package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// ErrBudgetExceeded is returned by chat operations when the tenant has reached
// its configured daily AI spend cap.
var ErrBudgetExceeded = errors.New("ai budget exceeded for tenant")

// Chat is the RAG service. It runs a retrieval (vector/bm25/hybrid) using
// Search, formats the context, calls the LLM, and returns the answer plus
// citations. Every call records an ai_usage row for lineage.
type Chat struct {
	search                *Search
	llm                   LLM
	repo                  repository.Repository
	logger                *slog.Logger
	promptMicrosPer1K     int64
	completionMicrosPer1K int64
	dailyBudgetMicros     int64
	perTenantBudget       bool
}

func NewChat(search *Search, llm LLM, repo repository.Repository, logger *slog.Logger) *Chat {
	if logger == nil {
		logger = slog.Default()
	}
	return &Chat{search: search, llm: llm, repo: repo, logger: logger}
}

// WithPricing sets per-1000-token prices (USD) used to estimate the cost
// recorded with each chat usage row. Zero prices (the default) record token
// counts and latency but a cost of 0.
func (c *Chat) WithPricing(promptUSDPer1K, completionUSDPer1K float64) *Chat {
	c.promptMicrosPer1K = usdPer1KToMicros(promptUSDPer1K)
	c.completionMicrosPer1K = usdPer1KToMicros(completionUSDPer1K)
	return c
}

// WithBudget sets the default per-tenant daily AI spend cap (USD). Zero (the
// default) disables enforcement. Requires pricing to be set to have any effect.
func (c *Chat) WithBudget(dailyUSD float64) *Chat {
	c.dailyBudgetMicros = int64(dailyUSD * 1_000_000) // USD → micros
	return c
}

// WithPerTenantBudgets lets each tenant override the global daily cap via its
// stored quota row (daily_budget_micros). When enabled, a tenant's override (when
// > 0) takes precedence over the global default — so a tenant cap can apply even
// when no global cap is set.
func (c *Chat) WithPerTenantBudgets() *Chat {
	c.perTenantBudget = true
	return c
}

// budgetMicros returns the effective daily cap for a tenant: its stored override
// when per-tenant budgets are enabled and the tenant set one (> 0), else the
// global default.
func (c *Chat) budgetMicros(ctx context.Context, tenant string) (int64, error) {
	if c.perTenantBudget && c.repo != nil {
		q, err := c.repo.GetTenantQuota(ctx, tenant)
		if err != nil {
			return 0, err
		}
		if q.DailyBudgetMicros > 0 {
			return q.DailyBudgetMicros, nil
		}
	}
	return c.dailyBudgetMicros, nil
}

// overBudget reports whether the tenant has reached its daily AI spend cap.
func (c *Chat) overBudget(ctx context.Context, tenant string) (bool, error) {
	budget, err := c.budgetMicros(ctx, tenant)
	if err != nil {
		return false, err
	}
	if budget <= 0 {
		return false, nil
	}
	since := time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339Nano)
	spent, err := c.repo.SumAICostMicros(ctx, tenant, since)
	if err != nil {
		return false, err
	}
	return spent >= budget, nil
}

// ChatReq is the user-facing input.
type ChatReq struct {
	Tenant      string
	Bucket      string
	Query       string
	K           int
	Mode        string // search mode: vector | bm25 | hybrid
	Temperature float64
	Caller      string
	ReqID       string
	Prior       []ChatMessage // optional conversation history (not retrieved)
}

// ChatResp is the answer + the chunks used.
type ChatResp struct {
	Answer    string `json:"answer"`
	Model     string `json:"model"`
	Citations []Hit  `json:"citations"`
}

const defaultSystemPrompt = `You are aero-vault, an assistant that answers questions using the provided knowledge base context. Cite sources inline as [#n] referring to the numbered chunks. If the context doesn't contain the answer, say so explicitly.`

func (c *Chat) buildChatPrompt(ctx context.Context, req ChatReq) ([]ChatMessage, []Hit, error) {
	if req.Query == "" {
		return nil, nil, fmt.Errorf("query required")
	}
	if req.K <= 0 || req.K > 20 {
		req.K = 5
	}
	if over, err := c.overBudget(ctx, req.Tenant); err != nil {
		return nil, nil, fmt.Errorf("budget check: %w", err)
	} else if over {
		return nil, nil, ErrBudgetExceeded
	}
	hits, err := c.search.Query(ctx, Request{
		Tenant: req.Tenant, Bucket: req.Bucket, Query: req.Query,
		K: req.K, Mode: req.Mode, Caller: req.Caller, ReqID: req.ReqID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("retrieval: %w", err)
	}
	var ctxBlock strings.Builder
	ctxBlock.WriteString("Knowledge base context:\n\n")
	for i, h := range hits {
		fmt.Fprintf(&ctxBlock, "[#%d] %s/%s (score %.3f)\n%s\n\n", i+1, h.Bucket, h.ObjectKey, h.Score, h.Chunk)
	}
	messages := []ChatMessage{
		{Role: "system", Content: defaultSystemPrompt},
		{Role: "system", Content: ctxBlock.String()},
	}
	messages = append(messages, req.Prior...)
	messages = append(messages, ChatMessage{Role: "user", Content: req.Query})
	return messages, hits, nil
}

// AnswerStream is the streaming variant. It invokes the LLM with the same
// retrieval prelude as Answer, but pipes each token chunk to onChunk as the
// model emits it. The final consolidated ChatResp is returned at the end.
func (c *Chat) AnswerStream(ctx context.Context, req ChatReq, onChunk func(string)) (ChatResp, error) {
	if c.llm == nil {
		return ChatResp{}, fmt.Errorf("chat disabled: no LLM configured")
	}
	if req.Caller == "" {
		req.Caller = "rest:chat-stream"
	}
	messages, hits, err := c.buildChatPrompt(ctx, req)
	if err != nil {
		return ChatResp{}, err
	}
	start := time.Now()
	resp, err := c.llm.ChatStream(ctx, ChatRequest{Messages: messages, Temperature: req.Temperature}, onChunk)
	if err != nil {
		return ChatResp{}, fmt.Errorf("llm stream: %w", err)
	}
	latency := time.Since(start)

	chunkIDs := make([]int64, 0, len(hits))
	objIDs := make([]int64, 0, len(hits))
	seen := map[int64]struct{}{}
	for _, h := range hits {
		chunkIDs = append(chunkIDs, h.ChunkID)
		if _, ok := seen[h.ObjectID]; !ok {
			seen[h.ObjectID] = struct{}{}
			objIDs = append(objIDs, h.ObjectID)
		}
	}
	pt, ct, tt := tokensFromUsage(resp.Usage)
	cost := costMicros(pt, ct, c.promptMicrosPer1K, c.completionMicrosPer1K)
	if err := c.repo.RecordUsage(ctx, repository.Usage{
		TenantID: req.Tenant, Caller: req.Caller, Query: req.Query,
		ChunkIDs: chunkIDs, ObjectIDs: objIDs, RequestID: req.ReqID,
		Model: resp.Model, PromptTokens: pt, CompletionTokens: ct, TotalTokens: tt,
		LatencyMs:  latency.Milliseconds(),
		CostMicros: cost,
	}); err != nil {
		c.logger.Warn("audit chat-stream usage", "err", err)
	}
	telemetry.RecordAIUsage(ctx, req.Tenant, resp.Model, pt, ct, cost)
	return ChatResp{Answer: resp.Content, Model: resp.Model, Citations: hits}, nil
}

func (c *Chat) Answer(ctx context.Context, req ChatReq) (ChatResp, error) {
	if c.llm == nil {
		return ChatResp{}, fmt.Errorf("chat disabled: no LLM configured")
	}
	if req.Caller == "" {
		req.Caller = "rest:chat"
	}
	messages, hits, err := c.buildChatPrompt(ctx, req)
	if err != nil {
		return ChatResp{}, err
	}
	start := time.Now()
	resp, err := c.llm.Chat(ctx, ChatRequest{
		Messages: messages, Temperature: req.Temperature,
	})
	if err != nil {
		return ChatResp{}, fmt.Errorf("llm: %w", err)
	}
	latency := time.Since(start)

	// Audit the chat call too — distinct caller value lets us slice usage.
	chunkIDs := make([]int64, 0, len(hits))
	objSeen := map[int64]struct{}{}
	objIDs := make([]int64, 0, len(hits))
	for _, h := range hits {
		chunkIDs = append(chunkIDs, h.ChunkID)
		if _, ok := objSeen[h.ObjectID]; !ok {
			objSeen[h.ObjectID] = struct{}{}
			objIDs = append(objIDs, h.ObjectID)
		}
	}
	pt, ct, tt := tokensFromUsage(resp.Usage)
	cost := costMicros(pt, ct, c.promptMicrosPer1K, c.completionMicrosPer1K)
	if err := c.repo.RecordUsage(ctx, repository.Usage{
		TenantID: req.Tenant, Caller: req.Caller, Query: req.Query,
		ChunkIDs: chunkIDs, ObjectIDs: objIDs, RequestID: req.ReqID,
		Model: resp.Model, PromptTokens: pt, CompletionTokens: ct, TotalTokens: tt,
		LatencyMs:  latency.Milliseconds(),
		CostMicros: cost,
	}); err != nil {
		c.logger.Warn("audit chat usage", "err", err)
	}
	telemetry.RecordAIUsage(ctx, req.Tenant, resp.Model, pt, ct, cost)

	return ChatResp{
		Answer:    resp.Content,
		Model:     resp.Model,
		Citations: hits,
	}, nil
}

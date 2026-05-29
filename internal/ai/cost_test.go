package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestTokensFromUsage(t *testing.T) {
	pt, ct, tt := tokensFromUsage(map[string]int{"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30})
	if pt != 10 || ct != 20 || tt != 30 {
		t.Fatalf("got (%d,%d,%d), want (10,20,30)", pt, ct, tt)
	}
	// total falls back to prompt+completion when absent.
	pt, ct, tt = tokensFromUsage(map[string]int{"prompt_tokens": 7, "completion_tokens": 5})
	if tt != 12 {
		t.Fatalf("total fallback: got %d, want 12", tt)
	}
	// empty map → zeros.
	if pt, ct, tt = tokensFromUsage(nil); pt != 0 || ct != 0 || tt != 0 {
		t.Fatalf("nil usage: got (%d,%d,%d), want zeros", pt, ct, tt)
	}
}

func TestCostMicros(t *testing.T) {
	// 100 prompt @ $2/1k = $0.20; 50 completion @ $6/1k = $0.30; total $0.50 = 500_000 micros.
	got := costMicros(100, 50, usdPer1KToMicros(2.0), usdPer1KToMicros(6.0))
	if got != 500_000 {
		t.Fatalf("costMicros = %d, want 500000", got)
	}
	// Unpriced → 0.
	if got := costMicros(100, 50, 0, 0); got != 0 {
		t.Fatalf("unpriced costMicros = %d, want 0", got)
	}
}

func TestUsdPer1KToMicros(t *testing.T) {
	if got := usdPer1KToMicros(2.0); got != 2_000_000 {
		t.Fatalf("usdPer1KToMicros(2.0) = %d, want 2000000", got)
	}
}

// usageLLM is a stub LLM that reports token usage, so we can assert the chat
// seam records cost/tokens/model.
type usageLLM struct{}

func (usageLLM) Name() string { return "stub-model" }
func (usageLLM) Chat(_ context.Context, _ ChatRequest) (ChatResponse, error) {
	return ChatResponse{
		Content: "the answer",
		Model:   "stub-model",
		Usage:   map[string]int{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
	}, nil
}
func (u usageLLM) ChatStream(ctx context.Context, req ChatRequest, onChunk func(string)) (ChatResponse, error) {
	onChunk("the answer")
	return u.Chat(ctx, req)
}

// TestChatRecordsCostAndTokens is the end-to-end seam test: a chat answer
// records token counts, model, latency, and estimated cost in ai_usage.
func TestChatRecordsCostAndTokens(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)

	obj := env.putObject(t, "kb.txt", "text/plain", "the answer is 42")
	env.seedChunks(t, obj, emb, "the answer is 42")

	search := NewSearch(env.repo, emb, nil)
	chat := NewChat(search, usageLLM{}, env.repo, nil).WithPricing(2.0, 6.0)

	if _, err := chat.Answer(ctx, ChatReq{
		Tenant: testTenant, Bucket: testBucket, Query: "the answer is 42",
		K: 3, Caller: "rest:chat", ReqID: "req-cost",
	}); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	usages, err := env.repo.ListUsageForObject(ctx, testTenant, obj.ID, 10)
	if err != nil {
		t.Fatalf("ListUsageForObject: %v", err)
	}
	if len(usages) == 0 {
		t.Fatal("expected a recorded usage row for the chat answer")
	}
	u := usages[0]
	if u.Model != "stub-model" {
		t.Errorf("model = %q, want stub-model", u.Model)
	}
	if u.PromptTokens != 100 || u.CompletionTokens != 50 || u.TotalTokens != 150 {
		t.Errorf("tokens = (%d,%d,%d), want (100,50,150)", u.PromptTokens, u.CompletionTokens, u.TotalTokens)
	}
	if u.CostMicros != 500_000 {
		t.Errorf("cost_micros = %d, want 500000 ($0.50)", u.CostMicros)
	}
	if u.LatencyMs < 0 {
		t.Errorf("latency_ms = %d, want >= 0", u.LatencyMs)
	}
}

// TestChatBudgetEnforced: once a tenant's recorded spend reaches the daily cap,
// further chat calls are rejected with ErrBudgetExceeded (the LLM is not called).
func TestChatBudgetEnforced(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "kb.txt", "text/plain", "the answer is 42")
	env.seedChunks(t, obj, emb, "the answer is 42")

	// Pre-spend $0.20 today.
	if err := env.repo.RecordUsage(ctx, repository.Usage{
		TenantID: testTenant, Caller: "rest:chat", CostMicros: 200_000, ObjectIDs: []int64{obj.ID},
	}); err != nil {
		t.Fatalf("seed usage: %v", err)
	}

	chat := NewChat(NewSearch(env.repo, emb, nil), usageLLM{}, env.repo, nil).
		WithPricing(2.0, 6.0).WithBudget(0.10) // $0.10/day cap — already exceeded

	if _, err := chat.Answer(ctx, ChatReq{Tenant: testTenant, Bucket: testBucket, Query: "x", K: 3}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
}

// TestChatBudgetAllowsUnderCap: a generous budget lets the call proceed.
func TestChatBudgetAllowsUnderCap(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "kb.txt", "text/plain", "the answer is 42")
	env.seedChunks(t, obj, emb, "the answer is 42")

	if err := env.repo.RecordUsage(ctx, repository.Usage{
		TenantID: testTenant, Caller: "rest:chat", CostMicros: 200_000, ObjectIDs: []int64{obj.ID},
	}); err != nil {
		t.Fatalf("seed usage: %v", err)
	}

	chat := NewChat(NewSearch(env.repo, emb, nil), usageLLM{}, env.repo, nil).
		WithPricing(2.0, 6.0).WithBudget(100.0) // $100/day cap — well under

	if _, err := chat.Answer(ctx, ChatReq{Tenant: testTenant, Bucket: testBucket, Query: "x", K: 3}); err != nil {
		t.Fatalf("under-budget call should proceed, got %v", err)
	}
}

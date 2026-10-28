package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Agent wraps the LLM in a tool-calling loop. It exposes list_files / read_file /
// search as functions and lets the model decide when to call them. Up to
// MaxSteps tool-call cycles are allowed before we force a final answer.
//
// This is conceptually similar to the MCP server, but lives behind a single
// /v1/agent endpoint and is meant for application code that doesn't speak
// MCP.
type Agent struct {
	svc      *service.FileService
	search   *Search
	llm      LLM
	repo     repository.Repository
	logger   *slog.Logger
	MaxSteps int
}

func NewAgent(svc *service.FileService, search *Search, llm LLM, repo repository.Repository, logger *slog.Logger) *Agent {
	if logger == nil {
		logger = slog.Default()
	}
	return &Agent{svc: svc, search: search, llm: llm, repo: repo, logger: logger, MaxSteps: 4}
}

// AgentReq is the user-facing input.
type AgentReq struct {
	Tenant string
	Query  string
	ReqID  string
}

// AgentResp is the final answer + the trace of tool calls.
type AgentResp struct {
	Answer string      `json:"answer"`
	Steps  []AgentStep `json:"steps"`
	Model  string      `json:"model"`
}

// AgentStep is one (tool call, result) pair.
type AgentStep struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
	Result string         `json:"result"`
}

const agentSystemPrompt = `You are an agent with access to a knowledge vault. Available tools:
- list_files(prefix, limit) — list object keys
- read_file(key) — return text content
- search(query, k) — semantic search, returns ranked chunks with source refs

Use tools when you need information; you may call several in a row. When you have enough context, write the final answer. Cite sources you used.`

// Run executes the loop. Every model call, forced-final call and final answer
// is gated by all resource refs accumulated from actual tool results.
func (a *Agent) Run(ctx context.Context, req AgentReq) (AgentResp, error) {
	if err := searchContextError(ctx); err != nil {
		return AgentResp{}, err
	}
	if nilLike(a.llm) {
		return AgentResp{}, fmt.Errorf("agent disabled: no LLM configured")
	}
	if req.Query == "" {
		return AgentResp{}, fmt.Errorf("query required")
	}
	guard := newAgentResourceGuard(a.svc, a.search, a.repo, req.Tenant)
	messages := []ChatMessage{
		{Role: "system", Content: agentSystemPrompt},
		{Role: "user", Content: req.Query},
	}
	tools := agentToolSpecs()
	var steps []AgentStep
	var lastModel string

	for step := 0; step < a.MaxSteps; step++ {
		if err := guard.validate(ctx); err != nil {
			return AgentResp{}, fmt.Errorf("agent resource guard: %w", err)
		}
		resp, err := a.llm.Chat(ctx, ChatRequest{Messages: append([]ChatMessage(nil), messages...), Tools: tools, ToolChoice: "auto"})
		if err != nil {
			return AgentResp{}, fmt.Errorf("llm step %d: %w", step, errors.Join(err, ctx.Err()))
		}
		lastModel = resp.Model
		if len(resp.ToolCalls) == 0 {
			if err := guard.validate(ctx); err != nil {
				return AgentResp{}, fmt.Errorf("agent final resource guard: %w", err)
			}
			return AgentResp{Answer: resp.Content, Steps: steps, Model: lastModel}, nil
		}
		toolMsgs, newSteps, err := a.executeToolCalls(ctx, guard, req.Tenant, resp.ToolCalls)
		if err != nil {
			return AgentResp{}, err
		}
		messages = append(messages, assistantToolMessage(resp))
		messages = append(messages, toolMsgs...)
		steps = append(steps, newSteps...)
	}
	return a.forcedFinal(ctx, guard, messages, steps, lastModel)
}

func (a *Agent) executeToolCalls(
	ctx context.Context,
	guard *agentResourceGuard,
	tenant string,
	calls []ToolCall,
) ([]ChatMessage, []AgentStep, error) {
	toolMsgs := make([]ChatMessage, 0, len(calls))
	steps := make([]AgentStep, 0, len(calls))
	for _, tc := range calls {
		if err := guard.validate(ctx); err != nil {
			return nil, nil, fmt.Errorf("agent pre-tool resource guard: %w", err)
		}
		name, _ := tc.Function["name"].(string)
		argsStr, _ := tc.Function["arguments"].(string)
		args := decodeToolArgs(argsStr)
		result, err := a.dispatchTool(ctx, guard, tenant, name, args)
		if err != nil {
			return nil, nil, fmt.Errorf("agent tool %s: %w", name, err)
		}
		if err := guard.validate(ctx); err != nil {
			return nil, nil, fmt.Errorf("agent post-tool resource guard: %w", err)
		}
		steps = append(steps, AgentStep{Tool: name, Args: args, Result: result})
		toolMsgs = append(toolMsgs, ChatMessage{Role: "tool", Content: result})
	}
	return toolMsgs, steps, nil
}

func (a *Agent) forcedFinal(
	ctx context.Context,
	guard *agentResourceGuard,
	messages []ChatMessage,
	steps []AgentStep,
	lastModel string,
) (AgentResp, error) {
	messages = append(messages, ChatMessage{Role: "system", Content: "Step budget exhausted. Provide the final answer now."})
	if err := guard.validate(ctx); err != nil {
		return AgentResp{}, fmt.Errorf("agent forced-final resource guard: %w", err)
	}
	resp, err := a.llm.Chat(ctx, ChatRequest{Messages: append([]ChatMessage(nil), messages...)})
	if err != nil {
		return AgentResp{}, fmt.Errorf("forced final answer: %w", errors.Join(err, ctx.Err()))
	}
	if err := guard.validate(ctx); err != nil {
		return AgentResp{}, fmt.Errorf("agent forced-final output guard: %w", err)
	}
	return AgentResp{Answer: resp.Content, Steps: steps, Model: resp.Model}, nil
}

func assistantToolMessage(resp ChatResponse) ChatMessage {
	assistantTC, _ := json.Marshal(map[string]any{"tool_calls": resp.ToolCalls})
	content := string(assistantTC)
	if resp.Content != "" {
		content = resp.Content + "\n" + content
	}
	return ChatMessage{Role: "assistant", Content: content}
}

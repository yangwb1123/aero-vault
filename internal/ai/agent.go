package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

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

// Run executes the loop.
func (a *Agent) Run(ctx context.Context, req AgentReq) (AgentResp, error) {
	if a.llm == nil {
		return AgentResp{}, fmt.Errorf("agent disabled: no LLM configured")
	}
	if req.Query == "" {
		return AgentResp{}, fmt.Errorf("query required")
	}
	tools := []ToolSpec{
		{Type: "function", Function: map[string]any{
			"name":        "list_files",
			"description": "List object keys, optionally filtered by prefix.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prefix": map[string]any{"type": "string"},
					"limit":  map[string]any{"type": "integer"},
				},
			},
		}},
		{Type: "function", Function: map[string]any{
			"name":        "read_file",
			"description": "Read an object's text content (truncated to 4KB).",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"key": map[string]any{"type": "string"}},
				"required":   []string{"key"},
			},
		}},
		{Type: "function", Function: map[string]any{
			"name":        "search",
			"description": "Semantic search across indexed chunks.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
					"k":     map[string]any{"type": "integer"},
				},
				"required": []string{"query"},
			},
		}},
	}
	messages := []ChatMessage{
		{Role: "system", Content: agentSystemPrompt},
		{Role: "user", Content: req.Query},
	}
	var steps []AgentStep
	var lastModel string

	for step := 0; step < a.MaxSteps; step++ {
		resp, err := a.llm.Chat(ctx, ChatRequest{
			Messages: messages, Tools: tools, ToolChoice: "auto",
		})
		if err != nil {
			return AgentResp{}, fmt.Errorf("llm step %d: %w", step, err)
		}
		lastModel = resp.Model
		if len(resp.ToolCalls) == 0 {
			return AgentResp{Answer: resp.Content, Steps: steps, Model: lastModel}, nil
		}
		// Append the LLM's tool-call message verbatim into the history.
		toolMsgs := make([]ChatMessage, 0, len(resp.ToolCalls)+1)
		// The OpenAI shape expects role="assistant" + tool_calls in the same message.
		assistantTC, _ := json.Marshal(map[string]any{"tool_calls": resp.ToolCalls})
		toolMsgs = append(toolMsgs, ChatMessage{Role: "assistant", Content: string(assistantTC)})

		for _, tc := range resp.ToolCalls {
			name, _ := tc.Function["name"].(string)
			argsStr, _ := tc.Function["arguments"].(string)
			var args map[string]any
			_ = json.Unmarshal([]byte(argsStr), &args)
			result := a.dispatchTool(ctx, req.Tenant, name, args)
			steps = append(steps, AgentStep{Tool: name, Args: args, Result: result})
			toolMsgs = append(toolMsgs, ChatMessage{Role: "tool", Content: result})
		}
		messages = append(messages, toolMsgs...)
	}
	// Force final answer
	messages = append(messages, ChatMessage{Role: "system", Content: "Step budget exhausted. Provide the final answer now."})
	resp, err := a.llm.Chat(ctx, ChatRequest{Messages: messages})
	if err != nil {
		return AgentResp{}, fmt.Errorf("forced final answer: %w", err)
	}
	return AgentResp{Answer: resp.Content, Steps: steps, Model: resp.Model}, nil
}

func (a *Agent) callListFiles(ctx context.Context, tenant string, args map[string]any) string {
	prefix, _ := args["prefix"].(string)
	limit := 20
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	}
	page, err := a.svc.List(ctx, tenant, service.DefaultBucket, prefix, "", limit)
	if err != nil {
		return "error: " + err.Error()
	}
	var b strings.Builder
	for _, o := range page.Objects {
		fmt.Fprintf(&b, "%s\t%d bytes\n", o.Key, o.Size)
	}
	if b.Len() == 0 {
		return "(no objects)"
	}
	return b.String()
}

func (a *Agent) callReadFile(ctx context.Context, tenant string, args map[string]any) string {
	key, _ := args["key"].(string)
	if key == "" {
		return "error: key required"
	}
	rc, _, err := a.svc.Get(ctx, tenant, service.DefaultBucket, key)
	if err != nil {
		return "error: " + err.Error()
	}
	defer rc.Close()
	body, _ := io.ReadAll(io.LimitReader(rc, 4<<10))
	return string(body)
}

func (a *Agent) callSearch(ctx context.Context, tenant string, args map[string]any) string {
	if a.search == nil {
		return "error: search not enabled"
	}
	q, _ := args["query"].(string)
	k := 5
	if v, ok := args["k"].(float64); ok {
		k = int(v)
	}
	if k <= 0 || k > 100 {
		k = 5
	}
	hits, err := a.search.Query(ctx, Request{Tenant: tenant, Query: q, K: k, Mode: "hybrid", Caller: "agent:search"})
	if err != nil {
		return "error: " + err.Error()
	}
	var b strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&b, "[#%d] %s/%s#%d\n%s\n", i+1, h.Bucket, h.ObjectKey, h.Seq, h.Chunk)
	}
	return b.String()
}

func (a *Agent) dispatchTool(ctx context.Context, tenant, name string, args map[string]any) string {
	switch name {
	case "list_files":
		return a.callListFiles(ctx, tenant, args)
	case "read_file":
		return a.callReadFile(ctx, tenant, args)
	case "search":
		return a.callSearch(ctx, tenant, args)
	default:
		return "error: unknown tool " + name
	}
}

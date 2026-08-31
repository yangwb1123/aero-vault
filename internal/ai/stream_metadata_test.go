package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestHTTPLLMChatStreamParsesUsageAndModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Model != "configured-model" {
			t.Errorf("request model = %q, want configured-model", req.Model)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"Hello"}}],"model":"provider-stream-model","usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"details":"ignored"}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[],"model":"","usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[],"model":"","usage":null}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	llm := NewHTTPLLM(srv.URL, "configured-model", "")
	var chunks []string
	resp, err := llm.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}, func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.Content != "Hello" {
		t.Fatalf("content = %q, want Hello", resp.Content)
	}
	if resp.Model != "provider-stream-model" {
		t.Fatalf("model = %q, want provider-stream-model", resp.Model)
	}
	wantUsage := map[string]int{
		"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150,
	}
	if !reflect.DeepEqual(resp.Usage, wantUsage) {
		t.Fatalf("usage = %#v, want %#v", resp.Usage, wantUsage)
	}
	if !reflect.DeepEqual(chunks, []string{"Hello"}) {
		t.Fatalf("chunks = %#v, want [Hello]", chunks)
	}
}

func TestHTTPLLMChatStreamFallsBackToRequestModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"answer"}}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	llm := NewHTTPLLM(srv.URL, "configured-model", "")
	resp, err := llm.ChatStream(context.Background(), ChatRequest{}, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.Model != "configured-model" {
		t.Fatalf("model = %q, want configured-model", resp.Model)
	}
}

func TestDecodeStreamUsageDistinguishesAbsentAndEmpty(t *testing.T) {
	usage, ok := decodeStreamUsage(nil)
	if ok || usage != nil {
		t.Fatalf("absent usage = %#v, %t; want nil, false", usage, ok)
	}
	usage, ok = decodeStreamUsage(json.RawMessage(`null`))
	if ok || usage != nil {
		t.Fatalf("null usage = %#v, %t; want nil, false", usage, ok)
	}
	usage, ok = decodeStreamUsage(json.RawMessage(`{}`))
	if !ok || usage == nil || len(usage) != 0 {
		t.Fatalf("empty usage = %#v, %t; want non-nil empty map, true", usage, ok)
	}
	usage, ok = decodeStreamUsage(json.RawMessage(`{"prompt_tokens":7,"bad":"ignored","none":null}`))
	if !ok || !reflect.DeepEqual(usage, map[string]int{"prompt_tokens": 7}) {
		t.Fatalf("filtered usage = %#v, %t", usage, ok)
	}
}

type streamingUsageLLM struct {
	calls int
	err   error
}

func (l *streamingUsageLLM) Name() string { return "configured-stream-model" }

func (l *streamingUsageLLM) Chat(context.Context, ChatRequest) (ChatResponse, error) {
	return l.response(), l.err
}

func (l *streamingUsageLLM) ChatStream(_ context.Context, _ ChatRequest, onChunk func(string)) (ChatResponse, error) {
	l.calls++
	if onChunk != nil {
		onChunk("streamed answer")
	}
	return l.response(), l.err
}

func (l *streamingUsageLLM) response() ChatResponse {
	return ChatResponse{
		Content: "streamed answer",
		Model:   "provider-stream-model",
		Usage: map[string]int{
			"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150,
		},
	}
}

func TestChatAnswerStreamRecordsCostAndTokens(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "stream-cost.txt", "text/plain", "stream cost knowledge")
	env.seedChunks(t, obj, emb, "stream cost knowledge retrieval")

	llm := &streamingUsageLLM{}
	chat := NewChat(NewSearch(env.repo, emb, nil), llm, env.repo, nil).
		WithPricing(2.0, 6.0)
	var streamed strings.Builder
	const caller = "rest:chat-stream"
	const requestID = "req-stream-cost"
	resp, err := chat.AnswerStream(ctx, ChatReq{
		Tenant: testTenant, Bucket: testBucket, Query: "stream cost retrieval",
		K: 3, Mode: "vector", Caller: caller, ReqID: requestID,
	}, func(chunk string) {
		streamed.WriteString(chunk)
	})
	if err != nil {
		t.Fatalf("AnswerStream: %v", err)
	}
	if resp.Answer != "streamed answer" || streamed.String() != "streamed answer" {
		t.Fatalf("answer/callback = %q/%q", resp.Answer, streamed.String())
	}
	usages, err := env.repo.ListUsageForObject(ctx, testTenant, obj.ID, 20)
	if err != nil {
		t.Fatalf("ListUsageForObject: %v", err)
	}
	var got *repositoryUsageView
	for _, usage := range usages {
		if usage.Caller == caller && usage.Model == "provider-stream-model" {
			copy := repositoryUsageView{
				TenantID: usage.TenantID, Caller: usage.Caller, Query: usage.Query,
				RequestID: usage.RequestID, Model: usage.Model,
				PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
				TotalTokens: usage.TotalTokens, CostMicros: usage.CostMicros,
				ChunkIDs: usage.ChunkIDs, ObjectIDs: usage.ObjectIDs,
			}
			got = &copy
			break
		}
	}
	if got == nil {
		t.Fatalf("stream usage row not found: %+v", usages)
	}
	if got.TenantID != testTenant || got.Caller != caller || got.Query != "stream cost retrieval" ||
		got.RequestID != requestID || got.Model != "provider-stream-model" {
		t.Fatalf("usage associations = %+v", got)
	}
	if got.PromptTokens != 100 || got.CompletionTokens != 50 || got.TotalTokens != 150 {
		t.Fatalf("usage tokens = (%d,%d,%d), want (100,50,150)", got.PromptTokens, got.CompletionTokens, got.TotalTokens)
	}
	if got.CostMicros != 500_000 {
		t.Fatalf("usage cost = %d, want 500000", got.CostMicros)
	}
	if len(got.ChunkIDs) == 0 || len(got.ObjectIDs) != 1 || got.ObjectIDs[0] != obj.ID {
		t.Fatalf("usage associations missing chunk/object: %+v", got)
	}
}

// repositoryUsageView keeps the persistence assertions in this test focused
// without changing the repository's public Usage type.
type repositoryUsageView struct {
	TenantID, Caller, Query, RequestID, Model   string
	PromptTokens, CompletionTokens, TotalTokens int
	CostMicros                                  int64
	ChunkIDs, ObjectIDs                         []int64
}

func TestChatAnswerStreamBudgetUsesPersistedCost(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "stream-budget.txt", "text/plain", "stream budget knowledge")
	env.seedChunks(t, obj, emb, "stream budget knowledge retrieval")
	llm := &streamingUsageLLM{}
	chat := NewChat(NewSearch(env.repo, emb, nil), llm, env.repo, nil).
		WithPricing(2.0, 6.0).WithBudget(0.5)
	req := ChatReq{Tenant: testTenant, Bucket: testBucket, Query: "stream budget retrieval", K: 3, Mode: "vector"}
	if _, err := chat.AnswerStream(ctx, req, nil); err != nil {
		t.Fatalf("first AnswerStream: %v", err)
	}
	if _, err := chat.AnswerStream(ctx, req, nil); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("second AnswerStream error = %v, want ErrBudgetExceeded", err)
	}
	if llm.calls != 1 {
		t.Fatalf("LLM calls = %d, want 1 after budget rejection", llm.calls)
	}
}

func TestChatAnswerStreamDoesNotRecordFailedStream(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	emb := NewHashEmbedder(64)
	obj := env.putObject(t, "stream-error.txt", "text/plain", "stream error knowledge")
	env.seedChunks(t, obj, emb, "stream error knowledge retrieval")
	llm := &streamingUsageLLM{err: errors.New("provider disconnected")}
	chat := NewChat(NewSearch(env.repo, emb, nil), llm, env.repo, nil).
		WithPricing(2.0, 6.0)
	if _, err := chat.AnswerStream(ctx, ChatReq{
		Tenant: testTenant, Bucket: testBucket, Query: "stream error retrieval",
		K: 3, Mode: "vector", Caller: "rest:chat-stream",
	}, nil); err == nil || !strings.Contains(err.Error(), "provider disconnected") {
		t.Fatalf("AnswerStream error = %v, want provider error", err)
	}
	usages, err := env.repo.ListUsageForObject(ctx, testTenant, obj.ID, 20)
	if err != nil {
		t.Fatalf("ListUsageForObject: %v", err)
	}
	for _, usage := range usages {
		if usage.Model == "provider-stream-model" {
			t.Fatalf("failed stream recorded usage: %+v", usage)
		}
	}
}

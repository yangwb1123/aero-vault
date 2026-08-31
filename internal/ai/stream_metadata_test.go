package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
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
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"Hello"}}],"model":"first-provider-model","usage":{"prompt_tokens":1,"initial_only":7,"nested":{"cached_tokens":1},"fractional":1.5,"details":"ignored"}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[],"model":"provider-stream-model","usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"latest_only":9,"nested":{"cached_tokens":2},"fractional":2.5}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[],"model":""}`+"\n\n")
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
		"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150, "latest_only": 9,
	}
	if !reflect.DeepEqual(resp.Usage, wantUsage) {
		t.Fatalf("usage = %#v, want %#v", resp.Usage, wantUsage)
	}
	if !reflect.DeepEqual(chunks, []string{"Hello"}) {
		t.Fatalf("chunks = %#v, want [Hello]", chunks)
	}
}

func TestHTTPLLMChatStreamUsageStateTransitions(t *testing.T) {
	initialUsage := `{"prompt_tokens":11,"initial_only":22}`
	cases := []struct {
		name      string
		frame     string
		wantUsage map[string]int
	}{
		{name: "absent", frame: `{"choices":[]}`, wantUsage: map[string]int{"prompt_tokens": 11, "initial_only": 22}},
		{name: "null", frame: `{"choices":[],"usage":null}`, wantUsage: map[string]int{"prompt_tokens": 11, "initial_only": 22}},
		{name: "empty object replaces", frame: `{"choices":[],"usage":{}}`, wantUsage: map[string]int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"choices":[],"model":"initial-model","usage":`+initialUsage+"}\n\n")
				_, _ = io.WriteString(w, "data: "+tc.frame+"\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()

			resp, err := NewHTTPLLM(srv.URL, "configured-model", "").ChatStream(
				context.Background(), ChatRequest{}, nil)
			if err != nil {
				t.Fatalf("ChatStream: %v", err)
			}
			if resp.Model != "initial-model" {
				t.Fatalf("model = %q, want initial-model", resp.Model)
			}
			if !reflect.DeepEqual(resp.Usage, tc.wantUsage) {
				t.Fatalf("usage = %#v, want %#v", resp.Usage, tc.wantUsage)
			}
		})
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

type streamRoundTripper func(*http.Request) (*http.Response, error)

func (f streamRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type streamBodyReadError struct {
	data []byte
	off  int
	err  error
}

func (b *streamBodyReadError) Read(p []byte) (int, error) {
	if b.off < len(b.data) {
		n := copy(p, b.data[b.off:])
		b.off += n
		return n, nil
	}
	return 0, b.err
}

func (b *streamBodyReadError) Close() error { return nil }

func TestHTTPLLMChatStreamReturnsPartialContentAndBodyError(t *testing.T) {
	bodyErr := errors.New("sentinel stream body read error")
	body := &streamBodyReadError{data: []byte(
		"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n" +
			"data: {malformed json\n\n"), err: bodyErr}
	llm := NewHTTPLLM("http://provider.invalid", "configured-model", "")
	llm.Client = &http.Client{Transport: streamRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: r,
		}, nil
	})}

	var chunks []string
	resp, err := llm.ChatStream(context.Background(), ChatRequest{}, func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if !errors.Is(err, bodyErr) {
		t.Fatalf("ChatStream error = %v, want body read error", err)
	}
	if resp.Content != "partial" {
		t.Fatalf("partial content = %q, want partial", resp.Content)
	}
	if !reflect.DeepEqual(chunks, []string{"partial"}) {
		t.Fatalf("partial callbacks = %#v, want [partial]", chunks)
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
	usage, ok = decodeStreamUsage(json.RawMessage(`{"prompt_tokens":7,"cached_tokens":4,"bad":"ignored","none":null,"nested":{"cached_tokens":1},"fractional":1.5}`))
	want := map[string]int{"prompt_tokens": 7, "cached_tokens": 4}
	if !ok || !reflect.DeepEqual(usage, want) {
		t.Fatalf("filtered usage = %#v, %t; want %#v", usage, ok, want)
	}
}

func TestDecodeStreamUsageRejectsNegativeAndOverflowCounts(t *testing.T) {
	usage, ok := decodeStreamUsage(json.RawMessage(`{"prompt_tokens":-1,"completion_tokens":3,"total_tokens":9223372036854775808,"negative_extension":-2}`))
	want := map[string]int{"completion_tokens": 3}
	if !ok || !reflect.DeepEqual(usage, want) {
		t.Fatalf("invalid usage = %#v, %t; want %#v", usage, ok, want)
	}
}

func TestTokenAccountingDoesNotOverflow(t *testing.T) {
	prompt, completion, total := tokensFromUsage(map[string]int{
		"prompt_tokens": math.MaxInt, "completion_tokens": math.MaxInt,
	})
	if prompt != math.MaxInt || completion != math.MaxInt || total != 0 {
		t.Fatalf("overflowing total = (%d,%d,%d), want (%d,%d,0)", prompt, completion, total, math.MaxInt, math.MaxInt)
	}
	negPrompt, negCompletion, negTotal := tokensFromUsage(map[string]int{
		"prompt_tokens": -1, "completion_tokens": 4, "total_tokens": -2,
	})
	if negPrompt != 0 || negCompletion != 4 || negTotal != 4 {
		t.Fatalf("negative usage fallback = (%d,%d,%d), want (0,4,4)", negPrompt, negCompletion, negTotal)
	}
	if got := costMicros(-1, 1, 2_000_000, 6_000_000); got != 0 {
		t.Fatalf("negative cost = %d, want 0", got)
	}
	if got := costMicros(math.MaxInt, 1, 2_000_000, 6_000_000); got != 0 {
		t.Fatalf("overflowing cost = %d, want 0", got)
	}
	if got := costMicros(math.MaxInt, math.MaxInt, 1_000, 1_000); got != 0 {
		t.Fatalf("overflowing cost sum = %d, want 0", got)
	}
}

type streamingUsageLLM struct {
	calls     int
	err       error
	omitTotal bool
	noUsage   bool
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
	var usage map[string]int
	if !l.noUsage {
		usage = map[string]int{
			"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150,
		}
		if l.omitTotal {
			delete(usage, "total_tokens")
		}
	}
	return ChatResponse{Content: "streamed answer", Model: "provider-stream-model", Usage: usage}
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
	if resp.Answer != "streamed answer" || resp.Model != "provider-stream-model" || streamed.String() != "streamed answer" {
		t.Fatalf("answer/model/callback = %q/%q/%q", resp.Answer, resp.Model, streamed.String())
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
				TotalTokens: usage.TotalTokens, LatencyMs: usage.LatencyMs,
				CostMicros: usage.CostMicros, ChunkIDs: usage.ChunkIDs, ObjectIDs: usage.ObjectIDs,
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
	if got.LatencyMs < 0 {
		t.Fatalf("usage latency = %d, want non-negative persisted latency", got.LatencyMs)
	}
	spent, err := env.repo.SumAICostMicros(ctx, testTenant, "1970-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("SumAICostMicros: %v", err)
	}
	if spent != got.CostMicros {
		t.Fatalf("summed cost = %d, want %d", spent, got.CostMicros)
	}
	if len(got.ChunkIDs) == 0 || len(got.ObjectIDs) != 1 || got.ObjectIDs[0] != obj.ID {
		t.Fatalf("usage associations missing chunk/object: %+v", got)
	}
}

func TestChatAnswerStreamUsageFallbackAndZero(t *testing.T) {
	cases := []struct {
		name, requestID         string
		llm                     streamingUsageLLM
		prompt, complete, total int
		cost                    int64
	}{
		{name: "total fallback", requestID: "req-stream-fallback", llm: streamingUsageLLM{omitTotal: true}, prompt: 100, complete: 50, total: 150, cost: 500_000},
		{name: "no usage", requestID: "req-stream-zero", llm: streamingUsageLLM{noUsage: true}, cost: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			emb := NewHashEmbedder(64)
			obj := env.putObject(t, "stream-usage.txt", "text/plain", "stream usage knowledge")
			env.seedChunks(t, obj, emb, "stream usage knowledge retrieval")
			chat := NewChat(NewSearch(env.repo, emb, nil), &tc.llm, env.repo, nil).
				WithPricing(2.0, 6.0)
			_, err := chat.AnswerStream(context.Background(), ChatReq{
				Tenant: testTenant, Bucket: testBucket, Query: "stream usage retrieval",
				K: 3, Mode: "vector", Caller: "rest:chat-stream", ReqID: tc.requestID,
			}, nil)
			if err != nil {
				t.Fatalf("AnswerStream: %v", err)
			}
			usages, err := env.repo.ListUsageForObject(context.Background(), testTenant, obj.ID, 20)
			if err != nil {
				t.Fatalf("ListUsageForObject: %v", err)
			}
			var got *repositoryUsageView
			for _, usage := range usages {
				if usage.RequestID == tc.requestID {
					copy := repositoryUsageView{
						PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
						TotalTokens: usage.TotalTokens, LatencyMs: usage.LatencyMs, CostMicros: usage.CostMicros,
					}
					got = &copy
					break
				}
			}
			if got == nil {
				t.Fatalf("stream usage row not found: %+v", usages)
			}
			if got.PromptTokens != tc.prompt || got.CompletionTokens != tc.complete ||
				got.TotalTokens != tc.total || got.CostMicros != tc.cost || got.LatencyMs < 0 {
				t.Fatalf("usage = %+v, want tokens (%d,%d,%d), cost %d, non-negative latency", got, tc.prompt, tc.complete, tc.total, tc.cost)
			}
			spent, err := env.repo.SumAICostMicros(context.Background(), testTenant, "1970-01-01T00:00:00Z")
			if err != nil {
				t.Fatalf("SumAICostMicros: %v", err)
			}
			if spent != tc.cost {
				t.Fatalf("summed cost = %d, want %d", spent, tc.cost)
			}
		})
	}
}

// repositoryUsageView keeps the persistence assertions in this test focused
// without changing the repository's public Usage type.
type repositoryUsageView struct {
	TenantID, Caller, Query, RequestID, Model   string
	PromptTokens, CompletionTokens, TotalTokens int
	LatencyMs, CostMicros                       int64
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

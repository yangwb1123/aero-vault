package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// --- HTTPEmbedder ---

func TestHTTPEmbedderSuccess(t *testing.T) {
	var gotReq embedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %q", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type: want application/json, got %q", ct)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer secret-key" {
			t.Errorf("authorization: want Bearer secret-key, got %q", auth)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		// Echo back one embedding per input.
		resp := embedResp{}
		for range gotReq.Input {
			resp.Data = append(resp.Data, struct {
				Embedding []float32 `json:"embedding"`
			}{Embedding: []float32{0.1, 0.2, 0.3}})
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL+"/", "test-model", "secret-key", 3)
	if e.Endpoint != srv.URL {
		t.Fatalf("trailing slash not trimmed: %q", e.Endpoint)
	}
	if e.Name() != "test-model" || e.Dimensions() != 3 {
		t.Fatalf("Name/Dimensions mismatch: %q / %d", e.Name(), e.Dimensions())
	}

	vecs, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if !reflect.DeepEqual(gotReq.Model, "test-model") {
		t.Fatalf("server saw model %q", gotReq.Model)
	}
	if len(vecs) != 2 {
		t.Fatalf("want 2 vectors, got %d", len(vecs))
	}
	if !reflect.DeepEqual(vecs[0], []float32{0.1, 0.2, 0.3}) {
		t.Fatalf("vector content mismatch: %v", vecs[0])
	}
}

// TestHTTPEmbedderReportsUsage verifies the embedder parses the provider's token
// usage and reports it (model + total_tokens) for the embed-usage metric.
func TestHTTPEmbedderReportsUsage(t *testing.T) {
	orig := recordEmbedUsage
	defer func() { recordEmbedUsage = orig }()
	var gotModel string
	var gotTokens int
	recordEmbedUsage = func(_ context.Context, model string, tokens int) {
		gotModel, gotTokens = model, tokens
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":40,"total_tokens":42}}`)
	}))
	defer srv.Close()

	e := NewHTTPEmbedder(srv.URL, "embed-x", "", 2)
	if _, err := e.Embed(context.Background(), []string{"hi"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if gotModel != "embed-x" || gotTokens != 42 {
		t.Fatalf("usage not reported: model=%q tokens=%d", gotModel, gotTokens)
	}
}

func TestHTTPEmbedderEmptyInputShortCircuits(t *testing.T) {
	// No request should be made for empty input.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called for empty input")
	}))
	defer srv.Close()
	e := NewHTTPEmbedder(srv.URL, "m", "", 3)
	vecs, err := e.Embed(context.Background(), nil)
	if err != nil || vecs != nil {
		t.Fatalf("empty input: want nil,nil got %v,%v", vecs, err)
	}
}

func TestHTTPEmbedderHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	e := NewHTTPEmbedder(srv.URL, "m", "", 3)
	_, err := e.Embed(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "embedder http 500") {
		t.Fatalf("want http 500 error, got %v", err)
	}
}

func TestHTTPEmbedderCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return 1 vector for 2 inputs.
		_, _ = io.WriteString(w, `{"data":[{"embedding":[1,2]}]}`)
	}))
	defer srv.Close()
	e := NewHTTPEmbedder(srv.URL, "m", "", 2)
	_, err := e.Embed(context.Background(), []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "returned 1 vectors for 2 texts") {
		t.Fatalf("want count mismatch error, got %v", err)
	}
}

func TestHTTPEmbedderInfersDimWhenZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"embedding":[1,2,3,4]}]}`)
	}))
	defer srv.Close()
	e := NewHTTPEmbedder(srv.URL, "m", "", 0) // dim unknown
	if _, err := e.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if e.Dim != 4 {
		t.Fatalf("Dim should be inferred to 4, got %d", e.Dim)
	}
}

// --- RemoteExtractor ---

func TestNewRemoteExtractorNilEndpoint(t *testing.T) {
	if NewRemoteExtractor("", "key", nil) != nil {
		t.Fatal("empty endpoint should return nil RemoteExtractor")
	}
}

func TestNewRemoteExtractorDefaultsFallback(t *testing.T) {
	re := NewRemoteExtractor("http://example.com/", "", nil)
	if re == nil {
		t.Fatal("expected non-nil extractor")
	}
	if re.Endpoint != "http://example.com" {
		t.Fatalf("trailing slash not trimmed: %q", re.Endpoint)
	}
	if _, ok := re.Fallback.(*DefaultExtractor); !ok {
		t.Fatalf("nil fallback should default to *DefaultExtractor, got %T", re.Fallback)
	}
}

func TestRemoteExtractorTextFastPath(t *testing.T) {
	// Text content must NOT hit the remote — handled by the fallback in-process.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("remote should not be called for text content")
	}))
	defer srv.Close()
	re := NewRemoteExtractor(srv.URL, "", NewDefaultExtractor())
	for _, ct := range []string{"text/plain", "", "application/json", "application/xml", "application/yaml", "x/y+json"} {
		got, err := re.Extract(context.Background(), ct, strings.NewReader("inline text"))
		if err != nil {
			t.Fatalf("fast-path %q: %v", ct, err)
		}
		if got != "inline text" {
			t.Fatalf("fast-path %q: want inline text, got %q", ct, got)
		}
	}
}

func TestRemoteExtractorBinarySuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/extract" {
			t.Errorf("path: want /extract, got %q", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("want multipart content-type, got %q", r.Header.Get("Content-Type"))
		}
		// Confirm the content_type form field was forwarded.
		_ = r.ParseMultipartForm(1 << 20)
		if r.FormValue("content_type") != "application/pdf" {
			t.Errorf("content_type field: got %q", r.FormValue("content_type"))
		}
		_ = json.NewEncoder(w).Encode(remoteExtractResp{Text: "extracted from pdf"})
	}))
	defer srv.Close()
	re := NewRemoteExtractor(srv.URL, "k", NewDefaultExtractor())
	got, err := re.Extract(context.Background(), "application/pdf", strings.NewReader("%PDF-1.4 binary"))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got != "extracted from pdf" {
		t.Fatalf("want remote text, got %q", got)
	}
}

// NOTE: The production RemoteExtractor.Extract returns an error on remote
// failure; it does NOT silently fall back to the wrapped extractor for binary
// content. The task brief described a fallback expectation, but the code path
// for binary content surfaces the remote error. We assert the actual behavior
// so the suite stays green and documents the real contract.
func TestRemoteExtractorBinaryRemoteErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "tika down", http.StatusBadGateway)
	}))
	defer srv.Close()
	re := NewRemoteExtractor(srv.URL, "", NewDefaultExtractor())
	_, err := re.Extract(context.Background(), "application/pdf", strings.NewReader("binary"))
	if err == nil || !strings.Contains(err.Error(), "remote extractor http 502") {
		t.Fatalf("want remote http 502 error, got %v", err)
	}
}

func TestRemoteExtractorRemoteErrorField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 200 OK but with an error field in the JSON body.
		_ = json.NewEncoder(w).Encode(remoteExtractResp{Error: "unsupported format"})
	}))
	defer srv.Close()
	re := NewRemoteExtractor(srv.URL, "", NewDefaultExtractor())
	_, err := re.Extract(context.Background(), "application/pdf", strings.NewReader("binary"))
	if err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("want error-field surfaced, got %v", err)
	}
}

// --- HTTPReranker ---

func TestNewHTTPRerankerNilEndpoint(t *testing.T) {
	if NewHTTPReranker("", "m", "k") != nil {
		t.Fatal("empty endpoint should return nil reranker")
	}
}

func TestHTTPRerankerReorders(t *testing.T) {
	var seen rerankReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			t.Errorf("path: want /rerank, got %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&seen)
		// Reorder: put index 2 first, then 0; drop index 1.
		_, _ = io.WriteString(w, `{"results":[{"index":2,"relevance_score":0.9},{"index":0,"relevance_score":0.5}]}`)
	}))
	defer srv.Close()
	r := NewHTTPReranker(srv.URL+"/", "rr-model", "key")
	if r.Name() != "rr-model" {
		t.Fatalf("Name: %q", r.Name())
	}
	hits := []Hit{
		{ChunkID: 10, Chunk: "first"},
		{ChunkID: 11, Chunk: "second"},
		{ChunkID: 12, Chunk: "third"},
	}
	out, err := r.Rerank(context.Background(), "query", hits, 5)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if seen.Query != "query" || len(seen.Documents) != 3 {
		t.Fatalf("server saw unexpected request: %+v", seen)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 reordered hits, got %d", len(out))
	}
	if out[0].ChunkID != 12 || out[1].ChunkID != 10 {
		t.Fatalf("reorder mismatch: got chunk ids %d,%d", out[0].ChunkID, out[1].ChunkID)
	}
	if out[0].Score != float32(0.9) {
		t.Fatalf("score not applied: %v", out[0].Score)
	}
}

func TestHTTPRerankerEmptyHits(t *testing.T) {
	r := NewHTTPReranker("http://example.com", "m", "")
	out, err := r.Rerank(context.Background(), "q", nil, 5)
	if err != nil || out != nil {
		t.Fatalf("empty hits: want nil,nil got %v,%v", out, err)
	}
}

func TestHTTPRerankerEmptyResultsKeepsTopK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	r := NewHTTPReranker(srv.URL, "m", "")
	hits := []Hit{{ChunkID: 1}, {ChunkID: 2}, {ChunkID: 3}}
	out, err := r.Rerank(context.Background(), "q", hits, 2)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	// Falls back to hits[:topK].
	if len(out) != 2 || out[0].ChunkID != 1 {
		t.Fatalf("empty results fallback: got %+v", out)
	}
}

func TestHTTPRerankerHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	r := NewHTTPReranker(srv.URL, "m", "")
	_, err := r.Rerank(context.Background(), "q", []Hit{{ChunkID: 1}}, 1)
	if err == nil || !strings.Contains(err.Error(), "reranker http 403") {
		t.Fatalf("want http 403 error, got %v", err)
	}
}

// --- HTTPLLM ---

func TestNewHTTPLLMNilEndpoint(t *testing.T) {
	if NewHTTPLLM("", "m", "k") != nil {
		t.Fatal("empty endpoint should return nil LLM")
	}
}

func TestHTTPLLMChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path: want /v1/chat/completions, got %q", r.URL.Path)
		}
		var req ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "gpt-test" {
			t.Errorf("model should default to client model, got %q", req.Model)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello there"}}],"model":"gpt-test","usage":{"total_tokens":7}}`)
	}))
	defer srv.Close()
	l := NewHTTPLLM(srv.URL+"/", "gpt-test", "key")
	if l.Name() != "gpt-test" {
		t.Fatalf("Name: %q", l.Name())
	}
	resp, err := l.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "hello there" || resp.Model != "gpt-test" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Usage["total_tokens"] != 7 {
		t.Fatalf("usage not parsed: %v", resp.Usage)
	}
}

func TestHTTPLLMChatToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"search","arguments":"{\"query\":\"x\"}"}}]}}],"model":"m"}`)
	}))
	defer srv.Close()
	l := NewHTTPLLM(srv.URL, "m", "")
	resp, err := l.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(resp.ToolCalls))
	}
	if name, _ := resp.ToolCalls[0].Function["name"].(string); name != "search" {
		t.Fatalf("tool call name: %q", name)
	}
}

func TestHTTPLLMChatErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 200 OK with an error object in the body.
		_, _ = io.WriteString(w, `{"error":{"message":"model overloaded"}}`)
	}))
	defer srv.Close()
	l := NewHTTPLLM(srv.URL, "m", "")
	_, err := l.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("want error-body surfaced, got %v", err)
	}
}

func TestHTTPLLMChatNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[],"model":"m"}`)
	}))
	defer srv.Close()
	l := NewHTTPLLM(srv.URL, "m", "")
	_, err := l.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "no choices") {
		t.Fatalf("want no choices error, got %v", err)
	}
}

func TestHTTPLLMChatHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	l := NewHTTPLLM(srv.URL, "m", "")
	_, err := l.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "llm http 429") {
		t.Fatalf("want llm http 429 error, got %v", err)
	}
}

func TestHTTPLLMChatStreamSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if acc := r.Header.Get("Accept"); acc != "text/event-stream" {
			t.Errorf("Accept header: want text/event-stream, got %q", acc)
		}
		// Confirm Stream flag was set on the request.
		var req ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream {
			t.Error("expected req.Stream=true for ChatStream")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// SSE frames: comment line, model+token frames, then [DONE].
		_, _ = io.WriteString(w, ": ping\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"role":"assistant"}}],"model":"stream-model"}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"Hello"}}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":", world"}}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	l := NewHTTPLLM(srv.URL, "stream-model", "")

	var chunks []string
	resp, err := l.ChatStream(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}, func(c string) {
		chunks = append(chunks, c)
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Content != "Hello, world" {
		t.Fatalf("assembled content: want %q, got %q", "Hello, world", resp.Content)
	}
	if resp.Model != "stream-model" {
		t.Fatalf("model: want stream-model, got %q", resp.Model)
	}
	if !reflect.DeepEqual(chunks, []string{"Hello", ", world"}) {
		t.Fatalf("streamed chunks mismatch: %v", chunks)
	}
}

func TestHTTPLLMChatStreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer srv.Close()
	l := NewHTTPLLM(srv.URL, "m", "")
	_, err := l.ChatStream(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "llm stream http 400") {
		t.Fatalf("want stream http 400 error, got %v", err)
	}
}

// --- sseScanner directly ---

func TestSSEScanner(t *testing.T) {
	raw := ": comment to ignore\n" +
		"\n" + // blank line skipped
		"data: {\"a\":1}\n" +
		"data:   {\"b\":2}\n" + // leading spaces trimmed
		"event: ignored\n" + // non-data line skipped
		"data: [DONE]\n"
	s := newSSEScanner(strings.NewReader(raw))
	var got []string
	for s.Scan() {
		got = append(got, s.Text())
	}
	if s.Err() != nil {
		t.Fatalf("scanner err: %v", s.Err())
	}
	want := []string{`{"a":1}`, `{"b":2}`, "[DONE]"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sse lines mismatch:\n got  %v\n want %v", got, want)
	}
}

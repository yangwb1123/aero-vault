package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/ai"
)

type providerMetadataStreamLLM struct{}

func (providerMetadataStreamLLM) Name() string { return "configured-stream-model" }

func (providerMetadataStreamLLM) Chat(context.Context, ai.ChatRequest) (ai.ChatResponse, error) {
	return ai.ChatResponse{Content: "unused", Model: "provider-stream-model"}, nil
}

func (providerMetadataStreamLLM) ChatStream(_ context.Context, _ ai.ChatRequest, onChunk func(string)) (ai.ChatResponse, error) {
	if onChunk != nil {
		onChunk("streamed answer")
	}
	return ai.ChatResponse{Content: "streamed answer", Model: "provider-stream-model"}, nil
}

func TestChatStreamSSEIncludesProviderModelAndAnswer(t *testing.T) {
	h := streamingHandler(t, providerMetadataStreamLLM{})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/stream", strings.NewReader(
		`{"query":"stream cancellation test","mode":"vector","k":5}`))
	rec := httptest.NewRecorder()

	h.ChatStream(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: token\ndata: \"streamed answer\"\n\n") {
		t.Fatalf("token event missing from %q", body)
	}
	if strings.Contains(body, "event: citations") {
		t.Fatalf("unexpected separate citations event in %q", body)
	}
	const donePrefix = "event: done\ndata: "
	start := strings.Index(body, donePrefix)
	if start < 0 {
		t.Fatalf("done event missing from %q", body)
	}
	payload := body[start+len(donePrefix):]
	if end := strings.Index(payload, "\n\n"); end >= 0 {
		payload = payload[:end]
	}
	var done ai.ChatResp
	if err := json.Unmarshal([]byte(payload), &done); err != nil {
		t.Fatalf("decode done payload %q: %v", payload, err)
	}
	if done.Answer != "streamed answer" || done.Model != "provider-stream-model" {
		t.Fatalf("done = %+v, want provider answer/model", done)
	}
	if len(done.Citations) == 0 {
		t.Fatal("done payload should retain retrieval citations")
	}
}

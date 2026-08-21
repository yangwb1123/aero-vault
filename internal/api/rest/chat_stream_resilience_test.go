package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

type cancelAwareStreamLLM struct {
	started  chan struct{}
	canceled chan struct{}
	start    sync.Once
	stop     sync.Once
}

func newCancelAwareStreamLLM() *cancelAwareStreamLLM {
	return &cancelAwareStreamLLM{started: make(chan struct{}), canceled: make(chan struct{})}
}

func (l *cancelAwareStreamLLM) Name() string { return "cancel-aware-test" }

func (l *cancelAwareStreamLLM) Chat(context.Context, ai.ChatRequest) (ai.ChatResponse, error) {
	return ai.ChatResponse{Content: "unused", Model: l.Name()}, nil
}

func (l *cancelAwareStreamLLM) ChatStream(ctx context.Context, _ ai.ChatRequest, onChunk func(string)) (ai.ChatResponse, error) {
	l.start.Do(func() { close(l.started) })
	for {
		select {
		case <-ctx.Done():
			l.stop.Do(func() { close(l.canceled) })
			return ai.ChatResponse{}, ctx.Err()
		default:
			onChunk("token")
			time.Sleep(time.Millisecond)
		}
	}
}

type failedStreamWriter struct{}

func (failedStreamWriter) Header() http.Header { return make(http.Header) }

func (failedStreamWriter) WriteHeader(int) {}

func (failedStreamWriter) Write([]byte) (int, error) {
	return 0, errors.New("client disconnected")
}

func (failedStreamWriter) Flush() {}

func streamingHandler(t *testing.T, llm ai.LLM) *AIHandler {
	t.Helper()
	svc, repo, _ := setupTest(t)
	obj := seedObjectForStream(t, svc)
	emb := ai.NewHashEmbedder(32)
	vecs, err := emb.Embed(context.Background(), []string{"stream cancellation test"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if err := repo.InsertChunks(context.Background(), []repository.Chunk{{
		ObjectID: obj.ID, TenantID: obj.TenantID, Bucket: obj.Bucket, ObjectKey: obj.Key,
		Seq: 0, Content: "stream cancellation test", Embedding: vecs[0],
		Dim: len(vecs[0]), EmbedModel: emb.Name(),
	}}); err != nil {
		t.Fatalf("insert chunk: %v", err)
	}
	search := ai.NewSearch(repo, emb, nil)
	chat := ai.NewChat(search, llm, repo, nil)
	return NewAIHandler(repo, nil, chat, nil, nil, false)
}

func seedObjectForStream(t *testing.T, svc *service.FileService) repository.Object {
	t.Helper()
	obj, err := svc.Put(context.Background(), "default", "default", "stream.txt", strings.NewReader("stream"), 6, service.PutOptions{ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("seed stream object: %v", err)
	}
	return obj
}

func TestChatStreamCancelsLLMWhenClientWriteFails(t *testing.T) {
	llm := newCancelAwareStreamLLM()
	h := streamingHandler(t, llm)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/stream", strings.NewReader(`{"query":"stream"}`))
	done := make(chan struct{})
	go func() {
		h.ChatStream(failedStreamWriter{}, r)
		close(done)
	}()

	select {
	case <-llm.started:
	case <-time.After(2 * time.Second):
		t.Fatal("LLM stream did not start")
	}
	select {
	case <-llm.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("client write failure did not cancel LLM context")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ChatStream did not return after client disconnect")
	}
}

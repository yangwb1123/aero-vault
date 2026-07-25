package events

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// captureHandler records the last request received by the mock server.
type captureHandler struct {
	mu     sync.Mutex
	body   []byte
	header http.Header
}

func (h *captureHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.body, _ = io.ReadAll(r.Body)
	h.header = r.Header.Clone()
	rw.WriteHeader(http.StatusOK)
}

// recordFailureRepo records calls to RecordWebhookFailure for assertions.
type recordFailureRepo struct {
	repository.Repository // nil embedded interface: any unexpected call panics
	mu                    sync.Mutex
	failures              []repository.WebhookFailure
}

func (r *recordFailureRepo) RecordWebhookFailure(_ context.Context, f repository.WebhookFailure) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, f)
	return int64(len(r.failures)), nil
}

func TestNewWebhook(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	w := NewWebhook("http://example.com/hook", logger)
	if len(w.urls) != 1 {
		t.Fatalf("expected 1 URL, got %d", len(w.urls))
	}
	if w.urls[0] != "http://example.com/hook" {
		t.Fatalf("unexpected URL: %s", w.urls[0])
	}
	if w.client == nil {
		t.Fatal("expected non-nil HTTP client")
	}

	w2 := NewWebhook(" http://a.com , http://b.com ", logger)
	if len(w2.urls) != 2 {
		t.Fatalf("expected 2 URLs, got %d", len(w2.urls))
	}
	if w2.urls[0] != "http://a.com" || w2.urls[1] != "http://b.com" {
		t.Fatalf("unexpected URLs: %v", w2.urls)
	}
}

func TestNewWebhook_NilLoggerDefaults(t *testing.T) {
	w := NewWebhook("http://example.com", nil)
	if w.logger == nil {
		t.Fatal("nil logger should default to slog.Default()")
	}
}

func TestNewWebhook_EmptyURL(t *testing.T) {
	w := NewWebhook("", nil)
	if len(w.urls) != 0 {
		t.Fatalf("expected 0 URLs for empty input, got %d", len(w.urls))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Run(ctx, nil)
}

func TestWebhookWithSecret(t *testing.T) {
	w := NewWebhook("http://example.com", slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.WithSecret("my-secret-key")
	if string(w.secret) != "my-secret-key" {
		t.Fatalf("expected secret 'my-secret-key', got '%s'", string(w.secret))
	}
}

func TestWebhookWithRetryStore(t *testing.T) {
	repo := &fakeRepo{}
	w := NewWebhook("http://example.com", slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.WithRetryStore(repo)
	if w.repo != repo {
		t.Fatal("WithRetryStore should set the repo field")
	}
}

func TestWebhookDeliver_SendsToMockServer(t *testing.T) {
	capt := &captureHandler{}
	srv := httptest.NewServer(capt)
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWebhook(srv.URL, logger)
	w.client = srv.Client()

	event := repository.Event{
		ID: 42, TenantID: "t1", Bucket: "b", Key: "k",
		Type: repository.EventCreated, RequestID: "req-1",
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	w.deliver(context.Background(), event)

	capt.mu.Lock()
	defer capt.mu.Unlock()

	if capt.body == nil {
		t.Fatal("mock server did not receive a request")
	}

	var got map[string]any
	if err := json.Unmarshal(capt.body, &got); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}

	if got["id"] != float64(42) {
		t.Fatalf("expected id=42, got %v", got["id"])
	}
	if got["tenant"] != "t1" {
		t.Fatalf("expected tenant=t1, got %v", got["tenant"])
	}
	if got["type"] != "created" {
		t.Fatalf("expected type=created, got %v", got["type"])
	}

	if ct := capt.header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %s", ct)
	}
}

func TestWebhookDeliverWithHMAC(t *testing.T) {
	secret := "test-hmac-secret"
	var (
		mu           sync.Mutex
		receivedBody []byte
		receivedSig  string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		receivedBody, _ = io.ReadAll(r.Body)
		receivedSig = r.Header.Get("X-Aero-Signature")
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWebhook(srv.URL, logger)
	w.WithSecret(secret)
	w.client = srv.Client()

	event := repository.Event{
		ID: 7, TenantID: "t1", Bucket: "b", Key: "k",
		Type: repository.EventDeleted, RequestID: "req-2",
		CreatedAt: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	w.deliver(context.Background(), event)

	mu.Lock()
	defer mu.Unlock()

	if receivedBody == nil {
		t.Fatal("mock server did not receive a request")
	}

	if receivedSig == "" {
		t.Fatal("expected X-Aero-Signature header, got empty")
	}
	if !strings.HasPrefix(receivedSig, "sha256=") {
		t.Fatalf("expected signature to start with sha256=, got %s", receivedSig)
	}
	expectedMAC := hmac.New(sha256.New, []byte(secret))
	expectedMAC.Write(receivedBody)
	expectedSig := "sha256=" + hex.EncodeToString(expectedMAC.Sum(nil))
	if receivedSig != expectedSig {
		t.Fatalf("HMAC signature mismatch:\n  got:  %s\n  want: %s", receivedSig, expectedSig)
	}
}

func TestWebhookDeliver_SendsEventIDHeaderWithHMAC(t *testing.T) {
	secret := "test-secret"
	var (
		mu         sync.Mutex
		receivedID string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		receivedID = r.Header.Get("X-Aero-Event-Id")
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWebhook(srv.URL, logger)
	w.WithSecret(secret)
	w.client = srv.Client()

	event := repository.Event{
		ID: 123, TenantID: "t1", Bucket: "b", Key: "k",
		Type: repository.EventCreated, CreatedAt: time.Now(),
	}
	w.deliver(context.Background(), event)

	mu.Lock()
	defer mu.Unlock()

	if receivedID != "123" {
		t.Fatalf("expected X-Aero-Event-Id=123, got %s", receivedID)
	}
}

func TestWebhookDeliver_MultipleURLs(t *testing.T) {
	var mu1, mu2 sync.Mutex
	var body1, body2 []byte

	srv1 := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu1.Lock()
		defer mu1.Unlock()
		body1, _ = io.ReadAll(r.Body)
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv1.Close()

	srv2 := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu2.Lock()
		defer mu2.Unlock()
		body2, _ = io.ReadAll(r.Body)
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWebhook(srv1.URL+","+srv2.URL, logger)
	w.client = srv1.Client()

	event := repository.Event{
		ID: 1, TenantID: "t1", Bucket: "b", Key: "k",
		Type: repository.EventCreated, CreatedAt: time.Now(),
	}
	w.deliver(context.Background(), event)

	mu1.Lock()
	mu2.Lock()
	defer mu1.Unlock()
	defer mu2.Unlock()

	if body1 == nil {
		t.Error("srv1 did not receive request")
	}
	if body2 == nil {
		t.Error("srv2 did not receive request")
	}
}

func TestWebhookDeliver_Non2xxPersistsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	repo := &recordFailureRepo{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWebhook(srv.URL, logger)
	w.client = srv.Client()
	w.repo = repo

	event := repository.Event{
		ID: 99, TenantID: "t1", Bucket: "b", Key: "k",
		Type: repository.EventCreated, CreatedAt: time.Now(),
	}
	w.deliver(context.Background(), event)

	repo.mu.Lock()
	defer repo.mu.Unlock()

	if len(repo.failures) != 1 {
		t.Fatalf("expected 1 persisted failure, got %d", len(repo.failures))
	}
	f := repo.failures[0]
	if f.EventID != 99 {
		t.Fatalf("expected EventID=99, got %d", f.EventID)
	}
	if f.URL != srv.URL {
		t.Fatalf("expected URL=%s, got %s", srv.URL, f.URL)
	}
	if f.LastStatus != 500 {
		t.Fatalf("expected LastStatus=500, got %d", f.LastStatus)
	}
	if f.Attempts != 1 {
		t.Fatalf("expected Attempts=1, got %d", f.Attempts)
	}
	if f.LastError != "non-2xx" {
		t.Fatalf("expected LastError='non-2xx', got '%s'", f.LastError)
	}
	if !f.NextRetryAt.After(time.Now()) {
		t.Fatal("expected NextRetryAt in the future")
	}
}

func TestWebhookDeliver_HTTPErrorPersistsFailure(t *testing.T) {
	repo := &recordFailureRepo{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWebhook("http://127.0.0.1:1/nonexistent", logger)
	w.repo = repo

	event := repository.Event{
		ID: 1, TenantID: "t1", Bucket: "b", Key: "k",
		Type: repository.EventCreated, CreatedAt: time.Now(),
	}
	w.deliver(context.Background(), event)

	repo.mu.Lock()
	defer repo.mu.Unlock()

	if len(repo.failures) != 1 {
		t.Fatalf("expected 1 persisted failure on connection error, got %d", len(repo.failures))
	}
	f := repo.failures[0]
	if f.EventID != 1 {
		t.Fatalf("expected EventID=1, got %d", f.EventID)
	}
	if f.LastError == "" {
		t.Fatal("expected non-empty LastError on connection failure")
	}
}

func TestWebhookDeliver_NoRepoDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWebhook(srv.URL, logger)
	w.client = srv.Client()

	event := repository.Event{
		ID: 1, TenantID: "t1", Bucket: "b", Key: "k",
		Type: repository.EventCreated, CreatedAt: time.Now(),
	}
	w.deliver(context.Background(), event)
}

func TestWebhookRun_DeliversFromSubscription(t *testing.T) {
	capt := &captureHandler{}
	srv := httptest.NewServer(capt)
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWebhook(srv.URL, logger)
	w.client = srv.Client()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := make(chan repository.Event, 4)
	go w.Run(ctx, sub)

	sub <- repository.Event{
		ID: 1, TenantID: "t", Bucket: "b", Key: "k",
		Type: repository.EventCreated, CreatedAt: time.Now(),
	}

	time.Sleep(50 * time.Millisecond)

	capt.mu.Lock()
	defer capt.mu.Unlock()
	if capt.body == nil {
		t.Fatal("Run did not deliver the event from subscription")
	}
}

func TestWebhookRun_ContextCancelStops(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWebhook("http://example.com", logger)

	ctx, cancel := context.WithCancel(context.Background())
	sub := make(chan repository.Event)

	done := make(chan struct{})
	go func() {
		w.Run(ctx, sub)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

func TestWebhookRun_ClosedSubscriptionExits(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWebhook("http://example.com", logger)

	ctx := context.Background()
	sub := make(chan repository.Event)

	done := make(chan struct{})
	go func() {
		w.Run(ctx, sub)
		close(done)
	}()

	close(sub)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after subscription channel closed")
	}
}

func TestWebhookRun_NoURLsReturnsImmediately(t *testing.T) {
	w := NewWebhook("", nil)
	sub := make(chan repository.Event)
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		w.Run(ctx, sub)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run with no URLs should return immediately")
	}
}

// postOne with an event ID that triggers fmtInt — ensure the X-Aero-Event-Id
// header is set correctly even for large/negative values.
func TestWebhookPostOne_CustomFmtInt(t *testing.T) {
	var (
		mu          sync.Mutex
		receivedID  string
		receivedSig string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		receivedID = r.Header.Get("X-Aero-Event-Id")
		receivedSig = r.Header.Get("X-Aero-Signature")
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := NewWebhook(srv.URL, logger)
	w.WithSecret("secret")
	w.client = srv.Client()

	body := []byte(`{"hello":"world"}`)
	w.postOne(context.Background(), 0, srv.URL, body, "sha256=abc", 1)

	mu.Lock()
	defer mu.Unlock()
	if receivedID != "0" {
		t.Fatalf("expected X-Aero-Event-Id=0, got %s", receivedID)
	}
	if receivedSig != "sha256=abc" {
		t.Fatalf("expected X-Aero-Signature=sha256=abc, got %s", receivedSig)
	}
}

func TestWebhookRetryLoop_NoRepoDoesNotPanic(t *testing.T) {
	w := NewWebhook("http://example.com", slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.RetryLoop(ctx)
}

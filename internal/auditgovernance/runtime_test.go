package auditgovernance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/repository"
)

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(value)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func runtimeConfig(baseURL string) config.AuditGovernanceConfig {
	cfg := publisherConfig(baseURL)
	cfg.Revision = 1
	cfg.HTTPTimeoutSeconds, cfg.PollMilliseconds = 1, 10
	cfg.BatchSize, cfg.ClaimTTLSeconds = 10, 3
	cfg.InitialBackoffSeconds, cfg.MaxBackoffSeconds = 1, 2
	cfg.MaxLagSeconds, cfg.ReconcileBatchSize = 4, 20
	cfg.DeliveredRetentionSeconds, cfg.CleanupIntervalSeconds = 3600, 60
	cfg.CleanupBatchSize = 20
	return cfg
}

func TestRuntimeRelaysAtomicAdminAndFileFactsAndDrains(t *testing.T) {
	var delivered atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":60}`))
			return
		}
		var id string
		body := struct {
			EventID string `json:"event_id"`
		}{}
		_ = jsonNewDecoder(r).Decode(&body)
		id = body.EventID
		delivered.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, `{"receipt":{"event_id":%q,"tenant_id":"acme","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z"}}`, id)
	}))
	defer server.Close()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := repo.(Store)
	runtime, err := New(runtimeConfig(server.URL), store,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	wrapped := WrapRepository(repo, runtime)
	if err := wrapped.RecordAudit(ctx, repository.AuditEntry{TenantID: "acme", Action: "tenant.status"}); err != nil {
		t.Fatalf("record audit: %v", err)
	}
	if _, err := wrapped.InsertEvent(ctx, repository.Event{TenantID: "acme", Bucket: "default",
		Key: "private", Type: repository.EventCreated}); err != nil {
		t.Fatalf("record event: %v", err)
	}
	runtime.Start(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for delivered.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	runtime.Close()
	if delivered.Load() != 2 {
		t.Fatalf("delivered=%d want=2", delivered.Load())
	}
	if _, ok, err := store.OldestPendingAuditGovernance(ctx); err != nil || ok {
		t.Fatalf("pending after drain ok=%v err=%v", ok, err)
	}
}

type requestJSONDecoder interface {
	Decode(any) error
}

func jsonNewDecoder(request *http.Request) requestJSONDecoder {
	return json.NewDecoder(request.Body)
}

func TestBoundedBackoffIsDeterministicAndCapped(t *testing.T) {
	first := boundedBackoff("fact-1", 20, time.Second, 5*time.Second)
	second := boundedBackoff("fact-1", 20, time.Second, 5*time.Second)
	if first != second || first < 500*time.Millisecond || first > 5*time.Second {
		t.Fatalf("backoff first=%v second=%v", first, second)
	}
}

func TestRuntimeRejectsRemovedBindingWithOpaqueBacklogReference(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "binding.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := runtimeConfig(server.URL)
	cfg.Bindings = append(cfg.Bindings, config.AuditGovernanceBinding{
		TenantID: "beta-private", ClientID: "beta-audit",
		ClientSecretEnv: "AUDIT_GOVERNANCE_CLIENT_SECRET_BETA", State: "active",
		ClientSecret: "beta-machine-secret",
	})
	runtime, err := New(cfg, repo.(Store), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	wrapped := WrapRepository(repo, runtime)
	if err := wrapped.RecordAudit(ctx, repository.AuditEntry{
		TenantID: "beta-private", Action: "tenant.status",
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Close()
	cfg.Revision = 2
	cfg.Bindings = cfg.Bindings[:1]
	_, err = New(cfg, repo.(Store), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "opaque:") ||
		strings.Contains(err.Error(), "beta-private") {
		t.Fatalf("unsafe removal error=%q", err)
	}
}

func TestRuntimeCredentialRotationRequiresHigherRevisionAndUsesNewSecret(t *testing.T) {
	secrets := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, secret, _ := r.BasicAuth()
			secrets <- secret
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":60}`))
			return
		}
		var body struct {
			EventID string `json:"event_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, `{"receipt":{"event_id":%q,"tenant_id":"acme","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z"}}`, body.EventID)
	}))
	defer server.Close()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "rotation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := runtimeConfig(server.URL)
	cfg.Bindings[0].ClientSecret = "old-machine-secret"
	old, err := New(cfg, repo.(Store), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	cfg.Bindings[0].ClientSecret = "rotated-machine-secret"
	if _, err := New(cfg, repo.(Store), slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("same-revision credential rotation was accepted")
	}
	cfg.Revision++
	current, err := New(cfg, repo.(Store), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := WrapRepository(repo, current).RecordAudit(ctx,
		repository.AuditEntry{TenantID: "acme", Action: "tenant.status"}); err != nil {
		t.Fatal(err)
	}
	current.Start(ctx)
	select {
	case got := <-secrets:
		if got != "rotated-machine-secret" {
			t.Fatalf("token endpoint received secret %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rotated credential was not used")
	}
	current.Close()
}

func TestRuntimeCloseDrainsBlockedHTTPDelivery(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":60}`))
			return
		}
		var body struct {
			EventID string `json:"event_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		once.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, `{"receipt":{"event_id":%q,"tenant_id":"acme","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z"}}`, body.EventID)
	}))
	defer server.Close()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "drain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(runtimeConfig(server.URL), repo.(Store),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := WrapRepository(repo, runtime).RecordAudit(ctx,
		repository.AuditEntry{TenantID: "acme", Action: "tenant.status"}); err != nil {
		t.Fatal(err)
	}
	runtime.Start(ctx)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("delivery did not start")
	}
	closed := make(chan struct{})
	go func() { runtime.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("runtime returned before blocked delivery drained")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not finish after delivery was released")
	}
	if _, ok, err := repo.(Store).OldestPendingAuditGovernance(ctx); err != nil || ok {
		t.Fatalf("pending after drain ok=%v err=%v", ok, err)
	}
}

func TestRelayLogsNeverExposeRawFactInputs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"raw-token","token_type":"Bearer","expires_in":60}`))
			return
		}
		http.Error(w, "raw-target raw-secret", http.StatusInternalServerError)
	}))
	defer server.Close()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var logs lockedBuffer
	runtime, err := New(runtimeConfig(server.URL), repo.(Store),
		slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	entry := repository.AuditEntry{TenantID: "acme", Actor: "raw-actor",
		Action: "key.add", Target: "raw-target", Detail: "raw-secret bearer-token"}
	if err := WrapRepository(repo, runtime).RecordAudit(ctx, entry); err != nil {
		t.Fatal(err)
	}
	runtime.Start(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "delivery deferred") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	runtime.Close()
	for _, forbidden := range []string{"acme", "raw-actor", "raw-target", "raw-secret",
		"bearer-token", "raw-token"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("relay log leaked %q: %s", forbidden, logs.String())
		}
	}
}

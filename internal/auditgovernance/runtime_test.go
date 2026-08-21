package auditgovernance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestRuntimeConflictingReceiptIsTerminalWithRetention(t *testing.T) {
	// Contract A: a conflict:true receipt is terminal-with-retention — the
	// relay fails the fact (never re-claims, never re-POSTs) and keeps the
	// row until the retention prune. Bounded-backoff retry forever is the
	// behavior this pins against.
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":60}`))
			return
		}
		posts.Add(1)
		var body struct {
			EventID string `json:"event_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, `{"receipt":{"event_id":%q,"tenant_id":"acme","status":"ledgered","accepted_at":"2026-08-04T00:00:00Z","conflict":true}}`, body.EventID)
	}))
	defer server.Close()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "conflict.db"))
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
	runtime.Start(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for posts.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if posts.Load() != 1 {
		t.Fatalf("first POST never happened: posts=%d", posts.Load())
	}
	// Terminal: further poll cycles must not re-POST the conflicting fact.
	time.Sleep(5 * time.Millisecond)
	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	runtime.Close()
	if got := posts.Load(); got != 1 {
		t.Fatalf("conflicting fact re-POSTed %d times, want exactly 1 (terminal)", got)
	}
	// Terminal rows are never re-claimed and are not pending backlog.
	if again, err := store.ClaimAuditGovernance(ctx, "observer", "token", 1, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("conflicting fact reclaimable: len=%d err=%v", len(again), err)
	}
	if _, ok, err := store.OldestPendingAuditGovernance(ctx); err != nil || ok {
		t.Fatalf("conflicting fact counts as pending ok=%v err=%v", ok, err)
	}
	// Retention: the failed row survives until the retention prune, then is
	// removed (terminal-with-retention).
	if n, err := store.CleanupFailedAuditGovernance(ctx, time.Now().Add(-time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("failed row pruned before retention window: n=%d err=%v", n, err)
	}
	if n, err := store.CleanupFailedAuditGovernance(ctx, time.Now().Add(time.Hour), 10); err != nil || n != 1 {
		t.Fatalf("failed row not pruned after retention window: n=%d err=%v", n, err)
	}
}

func TestBoundedBackoffIsDeterministicAndCapped(t *testing.T) {
	first := boundedBackoff("fact-1", 20, time.Second, 5*time.Second)
	second := boundedBackoff("fact-1", 20, time.Second, 5*time.Second)
	if first != second || first < 500*time.Millisecond || first > 5*time.Second {
		t.Fatalf("backoff first=%v second=%v", first, second)
	}
	// Contract pin (REQ-5.2): the 300 s default cap
	// (config_audit_governance.go:65) with 20 attempts — the doubling snaps
	// to the max (8 doublings: 256 s > max/2) and the ±25 % jitter clamps to
	// [225 s, 300 s], so > 200 s fails a broken doubling chain, not just a
	// missing cap.
	first = boundedBackoff("fact-1", 20, time.Second, 300*time.Second)
	second = boundedBackoff("fact-1", 20, time.Second, 300*time.Second)
	if first != second || first <= 200*time.Second || first > 300*time.Second {
		t.Fatalf("300s-cap backoff first=%v second=%v", first, second)
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

// TestRuntimeNewRejectsEmptyBindingsBeforeStoreIO pins AC-4.2: the
// fail-closed gate fires inside New's cfg.Validate() (runtime.go, before any
// store I/O) — a control revision of 0 on the migrated singleton proves
// applyDesiredBindings never ran. The probe: applying revision 1 directly
// succeeds only while control is still 0; a silent DELETE-all apply would
// have bumped control to 1 and drift-rejected the probe.
func TestRuntimeNewRejectsEmptyBindingsBeforeStoreIO(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := repo.(Store)
	cfg := runtimeConfig(server.URL)
	cfg.Bindings = nil
	_, err = New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "bindings") {
		t.Fatalf("empty desired bindings accepted by New, err=%v", err)
	}
	probe := []repository.AuditGovernanceBindingState{{
		TenantID: "acme", State: repository.AuditGovernanceBindingActive,
	}}
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "probe-digest", probe); err != nil {
		t.Fatalf("control revision != 0 after rejected New (apply must not have run): %v", err)
	}
	if safe, err := store.AuditGovernanceCanDisable(ctx); err != nil || safe {
		t.Fatalf("applied probe not visible: safe=%v err=%v", safe, err)
	}
}

// TestRuntimeDrainAppliesEmptyDesiredAndExposesMode pins AC-4.3's positive
// drain path and rule-3 observability: a drain boot (Drain=true + empty
// manifest, strictly higher revision) applies the DELETE-all and builds a
// Runtime whose Draining()/BoundTenants()/AppliedDigest() accessors describe
// the zero-tenant transition — the input to the WARN log and the
// bound_tenants/draining gauges. The rollback probe proves the apply bumped
// control revision past 1.
func TestRuntimeDrainAppliesEmptyDesiredAndExposesMode(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
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
	store := repo.(Store)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := runtimeConfig(server.URL)
	runtime, err := New(cfg, store, logger)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Draining() || runtime.BoundTenants() != 1 || runtime.AppliedDigest() == "" {
		t.Fatalf("healthy boot accessors draining=%v tenants=%d digest=%q",
			runtime.Draining(), runtime.BoundTenants(), runtime.AppliedDigest())
	}
	runtime.Close()
	cfg.Revision = 2
	cfg.Drain = true
	cfg.Bindings = nil
	runtime, err = New(cfg, store, logger)
	if err != nil {
		t.Fatalf("drain boot failed: %v", err)
	}
	if !runtime.Draining() || runtime.BoundTenants() != 0 || runtime.AppliedDigest() == "" {
		t.Fatalf("drain boot accessors draining=%v tenants=%d digest=%q",
			runtime.Draining(), runtime.BoundTenants(), runtime.AppliedDigest())
	}
	// Control revision advanced past 1: a rev-1 apply is now a rollback.
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "old-digest", nil); err == nil || !errors.Is(err, repository.ErrAuditGovernanceRevisionRollback) {
		t.Fatalf("control revision not bumped by drain apply, err=%v", err)
	}
	// Drained state is disable-safe: the disabled path can now pass.
	if safe, err := store.AuditGovernanceCanDisable(ctx); err != nil || !safe {
		t.Fatalf("post-drain disable safe=%v err=%v", safe, err)
	}
}

// TestDrainFlagWithNonEmptyManifestRefusesBoot pins rule 1 at the runtime
// seam (testing-review row, renamed): a drain flag with a non-empty manifest
// must refuse boot inside Validate — never a silent no-op — and leave the
// persisted binding state untouched.
func TestDrainFlagWithNonEmptyManifestRefusesBoot(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "armed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := repo.(Store)
	cfg := runtimeConfig(server.URL)
	if _, err := New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	cfg.Drain = true // non-empty manifest still bound
	_, err = New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "AUDIT_GOVERNANCE_DRAIN") {
		t.Fatalf("armed drain flag with non-empty manifest accepted, err=%v", err)
	}
	if safe, err := store.AuditGovernanceCanDisable(ctx); err != nil || safe {
		t.Fatalf("refused boot mutated persisted bindings: safe=%v err=%v", safe, err)
	}
}

// TestDrainRequiresStrictlyHigherRevision pins the drain+revision≤control
// refusal (testing-review row): a drain at the current control revision is a
// digest drift and must fail, leaving state unchanged. The wrapped error is
// asserted only as non-nil — the repo sentinel is masked by the generic
// "binding state initialization failed" wrapper — and the drift probe proves
// control is still at revision 1 with the original digest.
func TestDrainRequiresStrictlyHigherRevision(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "rev.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := repo.(Store)
	cfg := runtimeConfig(server.URL)
	if _, err := New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	cfg.Drain = true
	cfg.Bindings = nil
	cfg.Revision = 1 // not strictly higher than the applied control revision
	if _, err := New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("drain at the current control revision was accepted")
	}
	if safe, err := store.AuditGovernanceCanDisable(ctx); err != nil || safe {
		t.Fatalf("refused drain mutated persisted bindings: safe=%v err=%v", safe, err)
	}
	// Control is still revision 1 with the non-empty digest: a rev-1 apply
	// with a different digest drifts; only the original would pass.
	probe := []repository.AuditGovernanceBindingState{{
		TenantID: "acme", State: repository.AuditGovernanceBindingActive,
	}}
	if err := store.ApplyAuditGovernanceBindings(ctx, 1, "different-digest", probe); err == nil || !errors.Is(err, repository.ErrAuditGovernanceRevisionDrift) {
		t.Fatalf("control revision not preserved at 1, err=%v", err)
	}
}

// TestDrainRefusesWithOpaqueBacklogReference pins the drain+backlog refusal
// (testing-review row — the data-loss safety net): a drain with undelivered
// outbox rows must refuse startup via the existing unbound-backlog guard
// (never a drain-path bypass), with opaque refs only, and drop nothing.
func TestDrainRefusesWithOpaqueBacklogReference(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "drainbacklog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := repo.(Store)
	cfg := runtimeConfig(server.URL)
	runtime, err := New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := WrapRepository(repo, runtime).RecordAudit(ctx, repository.AuditEntry{
		TenantID: "acme", Action: "tenant.status",
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Close()
	cfg.Revision = 2
	cfg.Drain = true
	cfg.Bindings = nil
	_, err = New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "unbound backlog") ||
		!strings.Contains(err.Error(), "opaque:") || strings.Contains(err.Error(), "acme") {
		t.Fatalf("drain with pending facts error=%q", err)
	}
	if safe, err := store.AuditGovernanceCanDisable(ctx); err != nil || safe {
		t.Fatalf("refused drain dropped pending facts: safe=%v err=%v", safe, err)
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

// TestRuntimeReadyDegradesOnBacklogLag pins B3-2 (D1): a pending backlog
// beyond maxLag makes Ready() report degraded (nil) instead of failing
// /readyz, while BacklogAge exposes the age for the 450s alert gauge.
func TestRuntimeReadyDegradesOnBacklogLag(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "ready.db")
	repo, err := repository.Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, ok := repo.(Store)
	if !ok {
		t.Fatal("repo is not an audit governance store")
	}
	cfg := runtimeConfig("http://127.0.0.1:1")
	cfg.MaxLagSeconds = 4 // default; seeded fact is 2h old, far beyond maxLag
	// New() applies the configured acme binding (revision 1) internally.
	runtime, err := New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// Seed one pending fact that will never be delivered (server down). The
	// backlog age is measured from the outbox row's created_at (insert
	// time); backdatePendingFact rewrites it to 8s so the age crosses
	// maxLag (4s) deterministically — no sleeps.
	if _, err := store.InsertEventWithGovernance(ctx, repository.Event{
		TenantID: "acme", Bucket: "b", Key: "k", Type: repository.EventCreated,
		CreatedAt: time.Now().UTC(),
	}, repository.AuditGovernanceFact{SourceID: "acme", TenantID: "acme",
		OriginKind: repository.AuditOriginFile, FactKind: "file",
		Action: "file.create", OccurredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	backdatePendingFact(t, dsn, 8*time.Second)
	age, ok, err := runtime.PendingBacklogAge(ctx)
	if err != nil || !ok {
		t.Fatalf("BacklogAge ok=%v err=%v, want pending backlog", ok, err)
	}
	if age < 4*time.Second {
		t.Fatalf("BacklogAge=%v, want > maxLag (4s)", age)
	}
	if err := runtime.Ready(ctx); err != nil {
		t.Fatalf("Ready must degrade (nil) on maxLag, got %v", err)
	}
	// Acceptance (a) conjunction (REQ-1 residual): the probe has already
	// recorded the pair into the cache — Degraded()==true and BacklogAge()>maxLag.
	if !runtime.Degraded() {
		t.Fatal("Degraded()=false, want true after maxLag backdate")
	}
	if got := runtime.BacklogAge(); got <= 4*time.Second {
		t.Fatalf("BacklogAge()=%v, want > maxLag (4s)", got)
	}
	// Draining still fails readiness (unchanged gate).
	if err := store.ApplyAuditGovernanceBindings(ctx, 2, "acme-v2", []repository.AuditGovernanceBindingState{
		{TenantID: "acme", State: repository.AuditGovernanceBindingDraining},
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Ready(ctx); err == nil {
		t.Fatal("Ready must fail while a binding is draining")
	}
}

// TestRuntimeBacklogAgeZeroWhenNoPending pins the B3-1 interplay: terminal
// rows are excluded from the backlog, so a fully dead-lettered backlog
// reports BacklogAge ok=false (zero gauge) and never blocks readiness.
func TestRuntimeBacklogAgeZeroWhenNoPending(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "ready2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, ok := repo.(Store)
	if !ok {
		t.Fatal("repo is not an audit governance store")
	}
	cfg := runtimeConfig("http://127.0.0.1:1")
	runtime, err := New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := runtime.PendingBacklogAge(ctx); err != nil || ok {
		t.Fatalf("empty backlog: ok=%v err=%v", ok, err)
	}
	if err := runtime.Ready(ctx); err != nil {
		t.Fatalf("Ready on empty backlog: %v", err)
	}
}

package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/antivirus"
	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/replication"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/spaces"
)

func startupTestConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	return &config.Config{
		DB:      config.DBConfig{Driver: "sqlite", DSN: "file:" + filepath.Join(dir, "startup.db")},
		Storage: config.StorageConfig{Backend: "local", Local: config.LocalStorageConfig{Root: filepath.Join(dir, "objects")}},
		AI: config.AIConfig{Enabled: true, Dim: 8, ChunkWindow: 128, ChunkOverlap: 16,
			HybridSearch: true, ChatProvider: "mock", AgentMaxSteps: 3,
			EmbedCacheSize: 8, SearchCacheSize: 8, SearchCacheTTLSeconds: 30,
			ChatCostPromptPer1K: 0.01, ChatCostCompletionPer1K: 0.02, TenantDailyBudgetUSD: 1,
			PerTenantBudgets: true, RerankProvider: "heuristic", PIIScan: true, PIIRedact: true},
		Reconcile: config.ReconcileCfg{IntervalMinutes: 1, RetentionDays: 1,
			IdempotencyTTLHours: 1, UploadGCEnable: true, UploadGCHours: 1, ScrubEnabled: true},
	}
}

type startupObservedRepo struct {
	repository.Repository
	polls    atomic.Int64
	reads    atomic.Int64
	once     sync.Once
	poll     chan struct{}
	enqueued chan repository.Job
}

func (r *startupObservedRepo) NextUnconsumedEvents(context.Context, int) ([]repository.Event, error) {
	r.polls.Add(1)
	r.once.Do(func() { close(r.poll) })
	return nil, nil
}

func (r *startupObservedRepo) EnqueueJob(ctx context.Context, job repository.Job) (int64, bool, error) {
	id, deduped, err := r.Repository.EnqueueJob(ctx, job)
	if r.enqueued != nil && err == nil {
		r.enqueued <- job
	}
	return id, deduped, err
}

func (r *startupObservedRepo) CountEventOutbox(ctx context.Context) (int64, error) {
	r.reads.Add(1)
	return r.Repository.CountEventOutbox(ctx)
}

func startupTestPlan(t *testing.T, cfg *config.Config) (*spaceStartup, *startupObservedRepo, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	infra, err := prepareInfrastructure(ctx, cfg, logger)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	a, err := prepareSpaceAssembly(cfg, infra.raw, infra.store, infra.bus, infra.readers, logger)
	if err != nil {
		cancel()
		_ = infra.raw.Close()
		t.Fatal(err)
	}
	var p *spaceStartup
	t.Cleanup(func() {
		cancel()
		if p != nil {
			p.close()
		} else {
			a.close()
			infra.bus.Close()
			_ = infra.raw.Close()
		}
	})
	observed := &startupObservedRepo{Repository: a.repo, poll: make(chan struct{})}
	a.repo = observed
	infra.bus.WithRepository(observed)
	p, err = prepareSpaceStartup(cfg, logger, infra, a)
	if err != nil {
		t.Fatal(err)
	}
	return p, observed, ctx
}

func startupTestEndpoint(t *testing.T, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":true,"status":"ok"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSpaceStartupPreparationIsInert(t *testing.T) {
	cfg := startupTestConfig(t)
	var calls atomic.Int64
	srv := startupTestEndpoint(t, &calls)
	cfg.AI.VectorBackend, cfg.AI.VectorURL = "qdrant", srv.URL
	cfg.AI.ReindexStaleOnStart = true
	cfg.Events.WebhookURL = srv.URL
	cfg.Events.Transport, cfg.Events.TransportDSN = "postgres", "not-a-live-service"
	cfg.Antivirus.Enabled, cfg.Jobs.Workers = true, 1
	cfg.Replication = config.ReplicationCfg{Enabled: true, ResyncIntervalMinutes: 1,
		Storage: config.StorageConfig{Backend: "local", Local: config.LocalStorageConfig{Root: filepath.Join(t.TempDir(), "replica")}}}
	p, observed, ctx := startupTestPlan(t, cfg)
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	startupTestPreparedHandles(t, p)
	startupTestInertIO(t, cfg, p, observed, &calls, ctx)
	if types := p.registry.Types(); !slices.Contains(types, ai.JobIndexObject) || !slices.Contains(types, ai.JobDeleteChunks) || !slices.Contains(types, antivirus.JobScan) {
		t.Fatalf("missing full-job registrations: %v", types)
	}
}

func startupTestPreparedHandles(t *testing.T, p *spaceStartup) {
	t.Helper()
	if p.search == nil || p.chat == nil || p.agent == nil || p.bm25 == nil || p.indexer == nil || p.qdrant == nil {
		t.Fatal("AI handles were not prepared")
	}
	startupTestPreparedConsumers(t, p)
}

func startupTestPreparedConsumers(t *testing.T, p *spaceStartup) {
	t.Helper()
	if p.workers.av == nil || p.workers.notifier == nil || p.workers.webhook == nil || p.transport == nil {
		t.Fatal("enabled consumer handles were not prepared")
	}
	if p.workers.replica != nil || p.workers.initialized || p.initialized || p.started {
		t.Fatal("preparation initialized or started a backend")
	}
}

func startupTestInertIO(t *testing.T, cfg *config.Config, p *spaceStartup, observed *startupObservedRepo, calls *atomic.Int64, ctx context.Context) {
	t.Helper()
	if _, err := os.Stat(cfg.Replication.Storage.Local.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replica backend constructed before preflight: %v", err)
	}
	if err := p.start(ctx); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("uninitialized start = %v", err)
	}
	if calls.Load() != 0 || observed.polls.Load() != 0 || observed.reads.Load() != 0 {
		t.Fatal("preparation/uninitialized start performed backend, poll or backlog work")
	}
}

type startupBinding func(context.Context, string, string) (spaces.BindingResult, error)

func (f startupBinding) ProbeSpaceBinding(ctx context.Context, tenant, bucket string) (spaces.BindingResult, error) {
	return f(ctx, tenant, bucket)
}

type startupCoverage func(context.Context, spaces.CoverageScope) (spaces.CoverageResult, error)

func (f startupCoverage) ProbeSpaceCoverage(ctx context.Context, scope spaces.CoverageScope) (spaces.CoverageResult, error) {
	return f(ctx, scope)
}

type startupResources struct{ service.SpaceResourceReader }
type startupJobs struct{ repository.JobReader }
type startupOpaqueRepo struct{ repository.Repository }
type startupNilBoundary struct{ service.SpaceBoundary }

func TestSpaceStartupRawCapabilityFailures(t *testing.T) {
	p, _, _ := startupTestPlan(t, startupTestConfig(t))
	for _, raw := range []repository.Repository{nil, (*startupOpaqueRepo)(nil), &startupOpaqueRepo{Repository: p.infra.raw}} {
		_, err := captureSpaceReaders(raw)
		if !access.IsResourceBoundaryFault(err) || !errors.Is(err, repository.ErrJobReaderUnavailable) {
			t.Fatalf("capture hid missing raw JobReader cause: %v", err)
		}
	}
	original := p.infra.readers
	cases := []struct {
		name string
		edit func(*spaceReaders)
	}{
		{"binding_nil", func(r *spaceReaders) { r.router.Binding = nil }},
		{"binding_typed_nil", func(r *spaceReaders) { r.router.Binding = startupBinding(nil) }},
		{"coverage_nil", func(r *spaceReaders) { r.router.Coverage = nil }},
		{"coverage_typed_nil", func(r *spaceReaders) { r.router.Coverage = startupCoverage(nil) }},
		{"jobs_nil", func(r *spaceReaders) { r.router.Jobs = nil }},
		{"jobs_typed_nil", func(r *spaceReaders) { r.router.Jobs = (*startupJobs)(nil) }},
		{"resources_nil", func(r *spaceReaders) { r.router.Resources = nil }},
		{"resources_typed_nil", func(r *spaceReaders) { r.router.Resources = (*startupResources)(nil) }},
		{"authority_nil", func(r *spaceReaders) { r.router.EventAuthority = nil }},
		{"shares_nil", func(r *spaceReaders) { r.shares = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p.infra.readers = original
			tc.edit(&p.infra.readers)
			if err := p.validate(); !access.IsResourceBoundaryFault(err) {
				t.Fatalf("missing capability passed startup: %v", err)
			}
		})
	}
	p.infra.readers = original
}

func TestSpaceStartupConsumerPreflightPrecedesBackendIO(t *testing.T) {
	cases := []struct {
		name string
		edit func(*spaceStartup)
	}{
		{"service_unconfigured", func(p *spaceStartup) { p.assembly.svc.WithSpaceBoundary(nil) }},
		{"service_typed_nil", func(p *spaceStartup) { p.assembly.svc.WithSpaceBoundary((*startupNilBoundary)(nil)) }},
		{"search_binding_nil", func(p *spaceStartup) { p.search.WithResourceBoundary(nil, p.infra.readers.router.Coverage) }},
		{"search_resources_nil", func(p *spaceStartup) { p.search.WithCanonicalResources(nil) }},
		{"search_resources_typed_nil", func(p *spaceStartup) { p.search.WithCanonicalResources((*startupResources)(nil)) }},
		{"search_authorizer_nil", func(p *spaceStartup) { p.search.WithHitAuthorizer(nil) }},
		{"search_authorizer_typed_nil", func(p *spaceStartup) { p.search.WithHitAuthorizer((*service.FileService)(nil)) }},
		{"sink_nil", func(p *spaceStartup) { p.indexer.WithChunkSink(nil) }},
		{"sink_typed_nil", func(p *spaceStartup) { p.indexer.WithChunkSink((*ai.BM25)(nil)) }},
		{"reconcile_binding_nil", func(p *spaceStartup) { p.workers.maintenance.reconcile.WithResourceBoundary(nil) }},
		{"reconcile_coverage_nil", func(p *spaceStartup) { p.workers.maintenance.reconcile.WithOrphanCoverage(nil) }},
		{"lifecycle_binding_nil", func(p *spaceStartup) { p.workers.maintenance.lifecycle.WithResourceBoundary(nil) }},
		{"retention_binding_nil", func(p *spaceStartup) { p.workers.maintenance.retention.WithResourceBoundary(nil) }},
		{"upload_binding_nil", func(p *spaceStartup) { p.workers.maintenance.uploadGC.WithResourceBoundary(nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := startupTestConfig(t)
			var calls atomic.Int64
			cfg.AI.VectorBackend, cfg.AI.VectorURL = "qdrant", startupTestEndpoint(t, &calls).URL
			p, observed, ctx := startupTestPlan(t, cfg)
			tc.edit(p)
			if err := p.initialize(ctx); err == nil {
				t.Fatal("invalid consumer initialized")
			}
			if calls.Load() != 0 || observed.polls.Load() != 0 || p.initialized {
				t.Fatal("preflight failure reached backend/poll work")
			}
		})
	}
}

func TestSpaceStartupInitializedGuardsAndRealStarts(t *testing.T) {
	cfg := startupTestConfig(t)
	var calls atomic.Int64
	cfg.AI.VectorBackend, cfg.AI.VectorURL = "qdrant", startupTestEndpoint(t, &calls).URL
	cfg.Jobs.Workers = 1
	cfg.Replication = config.ReplicationCfg{Enabled: true, Storage: config.StorageConfig{
		Backend: "local", Local: config.LocalStorageConfig{Root: filepath.Join(t.TempDir(), "replica")}}}
	p, observed, ctx := startupTestPlan(t, cfg)
	if err := p.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	startupTestInitializedBackends(t, p, observed, &calls)
	p.workers.maintenance.lifecycle.WithResourceBoundary(nil)
	if err := p.start(ctx); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("late pre-start fault = %v", err)
	}
	if observed.polls.Load() != 0 || p.started || p.workers.started {
		t.Fatal("invalid initialized consumer started")
	}
	p.workers.maintenance.lifecycle.WithResourceBoundary(p.infra.readers.router.Binding)
	if err := p.start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed.poll:
	case <-time.After(5 * time.Second):
		t.Fatal("initialized production path never actually polled")
	}
	if err := p.start(ctx); !access.IsResourceBoundaryFault(err) {
		t.Fatalf("duplicate start accepted: %v", err)
	}
}

func startupTestInitializedBackends(t *testing.T, p *spaceStartup, observed *startupObservedRepo, calls *atomic.Int64) {
	t.Helper()
	if calls.Load() != 1 || observed.polls.Load() != 0 || p.started {
		t.Fatal("backend initialization did not remain separate from starts")
	}
	if p.workers.replica == nil || !slices.Contains(p.registry.Types(), replication.JobReplicate) {
		t.Fatal("real initialized replica/full-job handler missing")
	}
}

func TestSpaceStartupRejectsUnknownOrphanDeletion(t *testing.T) {
	cfg := startupTestConfig(t)
	infra, err := prepareInfrastructure(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { infra.bus.Close(); _ = infra.raw.Close() })
	coverage, err := infra.readers.router.Coverage.ProbeSpaceCoverage(t.Context(), spaces.CoverageScope{Kind: spaces.CoverageGlobal})
	if err != nil || !coverage.Complete || coverage.State != spaces.CoverageUnbound {
		t.Fatalf("fixture lacks complete all-unbound coverage: %+v %v", coverage, err)
	}
	cfg.Reconcile.DeleteOrphanBlobs = true
	_, err = prepareMaintenance(cfg, slog.Default(), infra.raw, infra.store, nil, infra.readers)
	if !access.IsResourceBoundaryFault(err) {
		t.Fatalf("unknown ownership became deletion authority: %v", err)
	}
}

func TestSpaceStartupOutboxNotifyRefusalDoesNotGateDeletedL2(t *testing.T) {
	cfg := startupTestConfig(t)
	received := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer root-test-token" || r.Header.Get("X-Audit-Fact-Id") == "" {
			http.Error(w, "bad fact receipt", http.StatusBadRequest)
			return
		}
		received <- string(body)
		w.Header().Set("X-Audit-Fact-Id", r.Header.Get("X-Audit-Fact-Id"))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	cfg.AuditSink = config.AuditSinkConfig{Kind: config.AuditSinkKindL2, Endpoint: srv.URL,
		Bindings: []config.AuditSinkL2Binding{{Tenant: e2eTenant, Token: "root-test-token"}}}
	cfg.EventOutbox = config.EventOutboxConfig{Enabled: true, PollMilliseconds: 3600000,
		BatchSize: 2, ClaimTTLSeconds: 30, HTTPTimeoutSeconds: 5, MaxAttempts: 3}
	p, _, _ := startupTestPlan(t, cfg)
	payload := startupTestSeedDeletedFacts(t, p)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); p.workers.relay.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	startupTestWaitOutboxTerminal(t, cfg.DB.DSN)
	select {
	case body := <-received:
		if body != string(payload) {
			t.Fatalf("L2 changed deleted fact bytes: %s", body)
		}
	default:
		t.Fatal("bound notify admission also blocked deleted L2 fact")
	}
}

func startupTestSeedDeletedFacts(t *testing.T, p *spaceStartup) []byte {
	t.Helper()
	space := startupTestSpace(t, p.infra.raw)
	obj, err := p.infra.raw.UpsertObject(t.Context(), repository.Object{TenantID: e2eTenant,
		Bucket: space.BackingBucket, Key: "gone.txt", VersionID: repository.NewVersionID(),
		Backend: "local", StorageKey: "exact-persisted-key", Size: 1, ETag: "etag"})
	if err != nil {
		t.Fatal(err)
	}
	payload := events.BuildDeletedFact(obj, "alice", "request", e2eTenant)
	facts := []repository.OutboxFact{
		{EventType: repository.EventTypeFileDeleted11, TenantID: e2eTenant, OriginID: obj.ID, Payload: payload},
		{EventType: repository.EventTypeFileNotify11, TenantID: e2eTenant, OriginID: obj.ID,
			Payload: events.BuildNotifyFact(obj, "alice", "request", e2eTenant, "sequencer")},
	}
	if err := p.infra.raw.HardDeleteObjectWithEvent(t.Context(), e2eTenant, obj.Bucket, obj.Key, repository.AuditEntry{}, facts); err != nil {
		t.Fatal(err)
	}
	return payload
}

func startupTestWaitOutboxTerminal(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var delivered, refused int
		err := db.QueryRow(`SELECT
		 (SELECT COUNT(*) FROM event_outbox WHERE event_type=? AND status='delivered' AND attempts=1 AND claim_owner='' AND claim_token=''),
		 (SELECT COUNT(*) FROM event_outbox WHERE event_type=? AND status='failed' AND attempts=1 AND claim_owner='' AND claim_token='')`,
			repository.EventTypeFileDeleted11, repository.EventTypeFileNotify11).Scan(&delivered, &refused)
		if err != nil {
			t.Fatal(err)
		}
		if delivered == 1 && refused == 1 {
			if startupTestSQLCount(t, dsn, "event_outbox_delivered") != 1 {
				t.Fatal("deleted L2 fact lacks fenced completion record")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("single initial relay batch did not deliver deleted/refuse notify facts")
}

func TestSpaceStartupServingRequiresCompletedStarts(t *testing.T) {
	p, observed, ctx := startupTestPlan(t, startupTestConfig(t))
	for _, serve := range []func() error{
		func() error { return servePreparedHTTP(ctx, p, nil) },
		func() error { return servePreparedMCP(ctx, p) },
	} {
		if err := serve(); !access.IsResourceBoundaryFault(err) {
			t.Fatalf("unstarted serving path admitted: %v", err)
		}
	}
	if observed.polls.Load() != 0 {
		t.Fatal("serving guard started workers")
	}
}

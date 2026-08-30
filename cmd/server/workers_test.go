package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/config"
	"github.com/aero-vault/aero-vault/internal/events"
	"github.com/aero-vault/aero-vault/internal/jobs"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// captureLogger returns a slog logger writing into buf (assertable text
// output, no stderr noise).
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

// ── AC-4: EVENT_OUTBOX_ENABLED=false gates the relay loop (D1/F1) ───────────

func openServerTestRepository(t *testing.T, ctx context.Context, dir string) repository.Repository {
	t.Helper()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "server.db"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate repository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func TestBuildBackgroundWorkersDoesNotOverridePrimaryDefaultStorageClass(t *testing.T) {
	previous := service.DefaultStorageClass
	t.Cleanup(func() { service.DefaultStorageClass = previous })
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			Backend:      "local",
			DefaultClass: "STANDARD",
			Local:        config.LocalStorageConfig{Root: filepath.Join(dir, "primary")},
		},
		Replication: config.ReplicationCfg{
			Enabled: true,
			Storage: config.StorageConfig{
				Backend:      "local",
				DefaultClass: "GLACIER",
				Local:        config.LocalStorageConfig{Root: filepath.Join(dir, "replica")},
			},
		},
		Jobs: config.JobsCfg{Workers: 1},
	}
	primary, err := buildStorage(ctx, cfg)
	if err != nil {
		t.Fatalf("build primary storage: %v", err)
	}
	if service.DefaultStorageClass != "STANDARD" {
		t.Fatalf("primary default = %q, want STANDARD", service.DefaultStorageClass)
	}
	repo := openServerTestRepository(t, ctx, dir)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New(repo, logger)
	t.Cleanup(func() {
		cancel()
		bus.Close()
	})
	registry := jobs.NewRegistry()
	queue := jobs.NewQueue(repo)
	svc := service.NewFileService(primary, repo, logger).WithEventSink(bus)
	if err := buildBackgroundWorkers(ctx, cfg, logger, repo, primary, bus, registry, queue, svc); err != nil {
		t.Fatalf("build background workers: %v", err)
	}
	if service.DefaultStorageClass != "STANDARD" {
		t.Fatalf("replication changed default to %q, want STANDARD", service.DefaultStorageClass)
	}

	const payload = "primary default"
	obj, err := svc.Put(ctx, "default", "default", "default.txt", strings.NewReader(payload), int64(len(payload)), service.PutOptions{})
	if err != nil {
		t.Fatalf("put object: %v", err)
	}
	stored, err := repo.GetObject(ctx, "default", "default", "default.txt")
	if err != nil {
		t.Fatalf("reload object: %v", err)
	}
	if obj.StorageClass != "STANDARD" || stored.StorageClass != "STANDARD" {
		t.Fatalf("storage classes = returned %q, persisted %q; want STANDARD", obj.StorageClass, stored.StorageClass)
	}
}

func TestBuildBackgroundWorkersReplicaBootstrapFailureDoesNotOverridePrimaryDefaultStorageClass(t *testing.T) {
	previous := service.DefaultStorageClass
	t.Cleanup(func() { service.DefaultStorageClass = previous })
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			Backend:      "local",
			DefaultClass: "STANDARD",
			Local:        config.LocalStorageConfig{Root: filepath.Join(dir, "primary")},
		},
		Replication: config.ReplicationCfg{
			Enabled: true,
			Storage: config.StorageConfig{Backend: "invalid", DefaultClass: "GLACIER"},
		},
		Jobs: config.JobsCfg{Workers: 1},
	}
	primary, err := buildStorage(ctx, cfg)
	if err != nil {
		t.Fatalf("build primary storage: %v", err)
	}
	repo := openServerTestRepository(t, ctx, dir)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New(repo, logger)
	t.Cleanup(func() {
		cancel()
		bus.Close()
	})
	err = buildBackgroundWorkers(ctx, cfg, logger, repo, primary, bus, jobs.NewRegistry(), jobs.NewQueue(repo), service.NewFileService(primary, repo, logger))
	if err == nil {
		t.Fatal("replica bootstrap unexpectedly succeeded")
	}
	if service.DefaultStorageClass != "STANDARD" {
		t.Fatalf("failed replication changed default to %q, want STANDARD", service.DefaultStorageClass)
	}
}

func TestBuildStorageBlankPrimaryDefaultUsesServiceFallback(t *testing.T) {
	previous := service.DefaultStorageClass
	t.Cleanup(func() { service.DefaultStorageClass = previous })
	service.WithDefaultStorageClass("GLACIER")
	cfg := &config.Config{Storage: config.StorageConfig{
		Backend: "local",
		Local:   config.LocalStorageConfig{Root: t.TempDir()},
	}}
	if _, err := buildStorage(context.Background(), cfg); err != nil {
		t.Fatalf("build primary storage: %v", err)
	}
	if service.DefaultStorageClass != primaryStorageClassFallback {
		t.Fatalf("blank primary default = %q, want fallback %q", service.DefaultStorageClass, primaryStorageClassFallback)
	}
}

func TestStartEventOutboxRelay_Disabled(t *testing.T) {
	ctx := context.Background()
	var buf bytes.Buffer
	cfg := &config.Config{}
	cfg.EventOutbox.Enabled = false
	// The gate precedes any repo or goroutine use: a nil repo must be safe —
	// the disabled branch logs the nil-repo-safe backlog=unknown and returns
	// nil without ever touching the repository (D6).
	if err := startEventOutboxRelay(ctx, cfg, captureLogger(&buf), nil); err != nil {
		t.Fatalf("disabled relay returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "event outbox relay disabled") {
		t.Errorf("log missing the disabled line:\n%s", out)
	}
	if !strings.Contains(out, "backlog=unknown") {
		t.Errorf("disabled line missing nil-repo-safe backlog=unknown:\n%s", out)
	}
	if strings.Contains(out, "event outbox relay started") {
		t.Errorf("disabled relay logged the started line:\n%s", out)
	}
}

// ── AC-4: fail-fast for a malformed L2 endpoint lives solely in
// Config.Validate(); the relay gate precedes the L2-sink build (F7) ──────────

func TestStartEventOutboxRelay_DisabledSkipsL2Build(t *testing.T) {
	ctx := context.Background()
	var buf bytes.Buffer
	cfg := &config.Config{}
	cfg.EventOutbox.Enabled = false
	cfg.AuditSinkL2.Endpoint = "not-a-url"
	if err := startEventOutboxRelay(ctx, cfg, captureLogger(&buf), nil); err != nil {
		t.Fatalf("disabled relay with malformed L2 endpoint returned error: %v", err)
	}
	if strings.Contains(buf.String(), "L2 audit sink enabled") {
		t.Error("disabled relay built the L2 audit sink")
	}
}

func TestAuditSinkForEventOutboxFollowsKind(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, kind := range []string{config.AuditSinkKindL0, config.AuditSinkKindL1} {
		sink, err := auditSinkForEventOutbox(&config.Config{AuditSink: config.AuditSinkConfig{Kind: kind}}, time.Second, logger)
		if err != nil || sink != nil {
			t.Fatalf("kind %s selected sink=%v err=%v, want no sink", kind, sink, err)
		}
	}
	sink, err := auditSinkForEventOutbox(&config.Config{AuditSink: config.AuditSinkConfig{
		Kind: config.AuditSinkKindL2, Endpoint: "https://audit.example.test",
	}}, time.Second, logger)
	if err != nil || sink == nil {
		t.Fatalf("bearer L2 selected sink=%v err=%v, want sink", sink, err)
	}
	_, err = auditSinkForEventOutbox(&config.Config{AuditSink: config.AuditSinkConfig{
		Kind: config.AuditSinkKindL2, Endpoint: "not-a-url",
	}}, time.Second, logger)
	if err == nil {
		t.Fatal("malformed bearer endpoint was accepted by the assembly seam")
	}
}

// ── AC-5: the started branch logs the live backlog (D6) ─────────────────────

func TestStartEventOutboxRelay_StartedLogsBacklog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "workers.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// One delete transaction writes exactly 2 facts (deleted@1.1 + notify@1.1).
	obj, err := repo.UpsertObject(ctx, repository.Object{
		TenantID: "default", Bucket: "default", Key: "w/a.txt", VersionID: "v-1",
		Backend: "local", StorageKey: "default/default/w/a.txt@v-1",
		Size: 7, ETag: "etag-1", ContentType: "text/plain",
	})
	if err != nil {
		t.Fatalf("seed object: %v", err)
	}
	if err := repo.HardDeleteObjectWithEvent(ctx, "default", "default", obj.Key, repository.AuditEntry{}, []repository.OutboxFact{
		{EventType: repository.EventTypeFileDeleted11, OriginID: obj.ID, TenantID: "default", Payload: []byte(`{"schema_version":"1.1"}`)},
		{EventType: repository.EventTypeFileNotify11, OriginID: obj.ID, TenantID: "default", Payload: []byte(`{"schema_version":"1.1"}`)},
	}); err != nil {
		t.Fatalf("seed facts: %v", err)
	}

	var buf bytes.Buffer
	cfg := &config.Config{}
	cfg.EventOutbox.Enabled = true // zero numerics → relay-side defaults
	if err := startEventOutboxRelay(ctx, cfg, captureLogger(&buf), repo); err != nil {
		t.Fatalf("start relay: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "event outbox relay started") {
		t.Errorf("log missing the started line:\n%s", out)
	}
	if !strings.Contains(out, "backlog=2") {
		t.Errorf("started line missing live backlog=2:\n%s", out)
	}
	if !strings.Contains(out, "delivered_retain_h=0") || !strings.Contains(out, "failed_retain_h=0") {
		t.Errorf("started line missing retention attrs:\n%s", out)
	}
}

package replication

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/jobs"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func newResyncFixture(t *testing.T) (context.Context, repository.Repository, *service.FileService, *jobs.Queue) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(dir, "resync.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return ctx, repo, service.NewFileService(store, repo, nil), jobs.NewQueue(repo)
}

func resyncLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func pendingReplicationJobs(t *testing.T, ctx context.Context, repo repository.Repository) []repository.Job {
	t.Helper()
	got, err := repo.ListJobs(ctx, repository.JobPending, JobReplicate, 20)
	if err != nil {
		t.Fatalf("list replication jobs: %v", err)
	}
	return got
}

func TestResyncEnqueuesMissing(t *testing.T) {
	ctx, repo, svc, queue := newResyncFixture(t)
	obj, err := svc.Put(ctx, "default", "default", "missing.txt", strings.NewReader("body"), 4,
		service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	resync := NewResyncer(repo, queue, 0, resyncLogger())
	resync.sweep(ctx)
	jobs := pendingReplicationJobs(t, ctx, repo)
	if len(jobs) != 1 {
		t.Fatalf("pending jobs=%d, want 1", len(jobs))
	}
	if jobs[0].TenantID != obj.TenantID || jobs[0].DedupeKey != "replicate:1" {
		t.Fatalf("job identity=%+v, want tenant=%q dedupe=replicate:%d", jobs[0], obj.TenantID, obj.ID)
	}
	id, err := DecodeObjectID(jobs[0].Payload)
	if err != nil || id != obj.ID {
		t.Fatalf("job payload id=%d err=%v, want %d", id, err, obj.ID)
	}
}

func TestResyncSkipsReplicated(t *testing.T) {
	ctx, repo, svc, queue := newResyncFixture(t)
	obj, err := svc.Put(ctx, "default", "default", "replicated.txt", strings.NewReader("body"), 4,
		service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := svc.SetObjectTagsByID(ctx, obj.ID, map[string]string{TagStatus: "replicated"}); err != nil {
		t.Fatalf("tag: %v", err)
	}

	NewResyncer(repo, queue, 0, resyncLogger()).sweep(ctx)
	if jobs := pendingReplicationJobs(t, ctx, repo); len(jobs) != 0 {
		t.Fatalf("pending jobs=%d, want 0", len(jobs))
	}
}

func TestResyncSkipsTerminalSkipped(t *testing.T) {
	ctx, repo, svc, queue := newResyncFixture(t)
	obj, err := svc.Put(ctx, "default", "default", "sse-c.txt", strings.NewReader("body"), 4,
		service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := svc.SetObjectTagsByID(ctx, obj.ID, map[string]string{TagStatus: "skipped"}); err != nil {
		t.Fatalf("tag: %v", err)
	}

	NewResyncer(repo, queue, 0, resyncLogger()).sweep(ctx)
	if jobs := pendingReplicationJobs(t, ctx, repo); len(jobs) != 0 {
		t.Fatalf("pending jobs=%d, want 0 for terminal skipped object", len(jobs))
	}
}

func TestResyncCoalescesWithPendingJob(t *testing.T) {
	ctx, repo, svc, queue := newResyncFixture(t)
	obj, err := svc.Put(ctx, "default", "default", "pending.txt", strings.NewReader("body"), 4,
		service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, deduped, err := queue.Enqueue(ctx, replicateJob(obj.TenantID, obj.ID)); err != nil || deduped {
		t.Fatalf("seed job: deduped=%v err=%v", deduped, err)
	}

	NewResyncer(repo, queue, 0, resyncLogger()).sweep(ctx)
	if jobs := pendingReplicationJobs(t, ctx, repo); len(jobs) != 1 {
		t.Fatalf("pending jobs=%d, want 1", len(jobs))
	}
}

func TestResyncCoversNonDefaultTenant(t *testing.T) {
	ctx, repo, svc, queue := newResyncFixture(t)
	if err := repo.UpsertTenant(ctx, repository.TenantRecord{TenantID: "acme"}); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	obj, err := svc.Put(ctx, "acme", "default", "tenant.txt", strings.NewReader("body"), 4,
		service.PutOptions{})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	NewResyncer(repo, queue, 0, resyncLogger()).sweep(ctx)
	jobs := pendingReplicationJobs(t, ctx, repo)
	if len(jobs) != 1 || jobs[0].TenantID != obj.TenantID {
		t.Fatalf("jobs=%+v, want one job for tenant %q", jobs, obj.TenantID)
	}
}

func TestResyncIntervalZero(t *testing.T) {
	ctx, repo, svc, queue := newResyncFixture(t)
	if _, err := svc.Put(ctx, "default", "default", "disabled.txt", strings.NewReader("body"), 4,
		service.PutOptions{}); err != nil {
		t.Fatalf("put: %v", err)
	}
	NewResyncer(repo, queue, 0, resyncLogger()).Run(ctx)
	if jobs := pendingReplicationJobs(t, ctx, repo); len(jobs) != 0 {
		t.Fatalf("pending jobs=%d, want 0 when interval is disabled", len(jobs))
	}
}

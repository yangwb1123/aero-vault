package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func testRepo(t *testing.T) repository.Repository {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "jobs.db")
	repo, err := repository.Open(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fastPool builds a pool with millisecond timings so retries don't slow tests.
func fastPool(repo repository.Repository, reg *Registry, workers int) *Pool {
	p := NewPool(repo, reg, workers, quietLogger())
	p.pollEvery = 2 * time.Millisecond
	p.baseBackoff = 2 * time.Millisecond
	p.maxBackoff = 20 * time.Millisecond
	p.reapEvery = 5 * time.Millisecond
	return p
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

func TestEnqueueClaimComplete(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	id, deduped, err := repo.EnqueueJob(ctx, repository.Job{Type: "noop", Payload: `{"x":1}`})
	if err != nil || deduped || id == 0 {
		t.Fatalf("enqueue: id=%d deduped=%v err=%v", id, deduped, err)
	}

	job, ok, err := repo.ClaimJob(ctx, "w0")
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if job.ID != id || job.Status != repository.JobRunning || job.Attempts != 1 {
		t.Fatalf("claimed wrong job: %+v", job)
	}
	if job.StartedAt.IsZero() {
		t.Fatalf("started_at not set")
	}

	// Queue is now empty.
	if _, ok, _ := repo.ClaimJob(ctx, "w0"); ok {
		t.Fatalf("expected empty queue")
	}

	if err := repo.CompleteJob(ctx, id, "done"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	jobs, err := repo.ListJobs(ctx, repository.JobSucceeded, "", 10)
	if err != nil || len(jobs) != 1 || jobs[0].Result != "done" {
		t.Fatalf("list succeeded: %v %+v", err, jobs)
	}
}

func TestDedupeCoalesces(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	id1, d1, err := repo.EnqueueJob(ctx, repository.Job{Type: "index_object", DedupeKey: "obj:7"})
	if err != nil || d1 {
		t.Fatalf("first enqueue: d=%v err=%v", d1, err)
	}
	id2, d2, err := repo.EnqueueJob(ctx, repository.Job{Type: "index_object", DedupeKey: "obj:7"})
	if err != nil {
		t.Fatalf("second enqueue: %v", err)
	}
	if !d2 || id2 != id1 {
		t.Fatalf("expected dedupe to existing id %d, got id=%d deduped=%v", id1, id2, d2)
	}
	all, _ := repo.ListJobs(ctx, "", "", 100)
	if len(all) != 1 {
		t.Fatalf("expected 1 row, got %d", len(all))
	}

	// After the live job completes, the dedupe key is free again.
	job, _, _ := repo.ClaimJob(ctx, "w0")
	_ = repo.CompleteJob(ctx, job.ID, "")
	id3, d3, err := repo.EnqueueJob(ctx, repository.Job{Type: "index_object", DedupeKey: "obj:7"})
	if err != nil || d3 || id3 == id1 {
		t.Fatalf("re-enqueue after completion: id=%d deduped=%v err=%v", id3, d3, err)
	}
}

func TestDedupeFailedJobDoesNotBlockReenqueue(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	id1, deduped, err := repo.EnqueueJob(ctx, repository.Job{
		Type: "replicate", DedupeKey: "replicate:9",
	})
	if err != nil || deduped {
		t.Fatalf("first enqueue: id=%d deduped=%v err=%v", id1, deduped, err)
	}
	job, claimed, err := repo.ClaimJob(ctx, "w0")
	if err != nil || !claimed || job.ID != id1 {
		t.Fatalf("claim: job=%+v claimed=%v err=%v", job, claimed, err)
	}
	if err := repo.FailJob(ctx, id1, "permanent failure"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	id2, deduped, err := repo.EnqueueJob(ctx, repository.Job{
		Type: "replicate", DedupeKey: "replicate:9",
	})
	if err != nil || deduped || id2 == id1 {
		t.Fatalf("re-enqueue failed job: id=%d deduped=%v err=%v", id2, deduped, err)
	}
}

func TestPoolRetriesThenSucceeds(t *testing.T) {
	repo := testRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	reg := NewRegistry()
	reg.Register("flaky", func(ctx context.Context, job repository.Job) error {
		if atomic.AddInt32(&calls, 1) < 3 {
			return errors.New("transient")
		}
		return nil
	})
	go fastPool(repo, reg, 2).Run(ctx)

	id, _, err := repo.EnqueueJob(ctx, repository.Job{Type: "flaky"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		jobs, _ := repo.ListJobs(ctx, "", "", 10)
		return len(jobs) == 1 && jobs[0].ID == id && jobs[0].Status == repository.JobSucceeded
	})
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 handler calls, got %d", got)
	}
}

func TestPoolFailsAfterMaxAttempts(t *testing.T) {
	repo := testRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	reg := NewRegistry()
	reg.Register("always_fails", func(ctx context.Context, job repository.Job) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("boom")
	})
	go fastPool(repo, reg, 1).Run(ctx)

	id, _, _ := repo.EnqueueJob(ctx, repository.Job{Type: "always_fails", MaxAttempts: 2})
	waitFor(t, 3*time.Second, func() bool {
		j, _ := repo.ListJobs(ctx, repository.JobFailed, "", 10)
		return len(j) == 1 && j[0].ID == id
	})
	jobs, _ := repo.ListJobs(ctx, "", "", 10)
	if jobs[0].Attempts != 2 || jobs[0].LastError == "" {
		t.Fatalf("expected 2 attempts + error, got %+v", jobs[0])
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 handler calls, got %d", got)
	}
}

func TestPoolUnknownTypeFails(t *testing.T) {
	repo := testRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go fastPool(repo, NewRegistry(), 1).Run(ctx)
	id, _, _ := repo.EnqueueJob(ctx, repository.Job{Type: "mystery"})
	waitFor(t, 2*time.Second, func() bool {
		j, _ := repo.ListJobs(ctx, repository.JobFailed, "", 10)
		return len(j) == 1 && j[0].ID == id
	})
}

func TestReapStuckJobs(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	id, _, _ := repo.EnqueueJob(ctx, repository.Job{Type: "noop"})
	if _, ok, err := repo.ClaimJob(ctx, "w0"); !ok || err != nil {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	// maxAge 0 => anything started at/before now is stuck.
	n, err := repo.ReapStuckJobs(ctx, 0)
	if err != nil || n != 1 {
		t.Fatalf("reap: n=%d err=%v", n, err)
	}
	jobs, _ := repo.ListJobs(ctx, repository.JobPending, "", 10)
	if len(jobs) != 1 || jobs[0].ID != id {
		t.Fatalf("expected requeued job %d, got %+v", id, jobs)
	}
}

func TestReapStuckJobAtMaxAttemptsFailsPermanently(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	id, _, err := repo.EnqueueJob(ctx, repository.Job{Type: "crashes", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repo.ClaimJob(ctx, "dead-worker"); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if n, err := repo.ReapStuckJobs(ctx, -time.Second); err != nil || n != 0 {
		t.Fatalf("reap requeued=%d err=%v, want 0", n, err)
	}
	failed, err := repo.ListJobs(ctx, repository.JobFailed, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].ID != id || failed[0].LastError == "" {
		t.Fatalf("exhausted stuck job was not failed: %+v", failed)
	}
}

func TestJobStats(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, _, _ = repo.EnqueueJob(ctx, repository.Job{Type: "noop"})
	}
	job, _, _ := repo.ClaimJob(ctx, "w0")
	_ = repo.CompleteJob(ctx, job.ID, "")
	stats, err := repo.JobStats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats[repository.JobPending] != 2 || stats[repository.JobSucceeded] != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

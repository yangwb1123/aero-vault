package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openJobsTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

// jobStatus returns the current status of the job with the given id, failing the
// test if it is missing.
func jobStatus(t *testing.T, repo repository.Repository, id int64) string {
	return jobRecord(t, repo, id).Status
}

func jobRecord(t *testing.T, repo repository.Repository, id int64) repository.Job {
	t.Helper()
	ctx := context.Background()
	jobs, err := repo.ListJobs(ctx, "", "", 500)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	for _, j := range jobs {
		if j.ID == id {
			return j
		}
	}
	t.Fatalf("job %d not found", id)
	return repository.Job{}
}

// enqueueAndClaim enqueues a single job and immediately claims it, returning the
// claimed job so the caller starts from a known 'running' state.
func enqueueAndClaim(t *testing.T, repo repository.Repository) repository.Job {
	t.Helper()
	ctx := context.Background()
	id, _, err := repo.EnqueueJob(ctx, repository.Job{Type: "t"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	j, ok, err := repo.ClaimJob(ctx, "worker-A")
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if j.ID != id {
		t.Fatalf("claimed id=%d, want %d", j.ID, id)
	}
	if j.Status != repository.JobRunning {
		t.Fatalf("claimed status=%q, want %q", j.Status, repository.JobRunning)
	}
	return j
}

// TestCompleteJobOnlyTransitionsRunning verifies the happy path (running →
// succeeded) and proves CompleteJob is a no-op once the reaper has bounced the
// job back to pending — the state-machine guard prevents a recovered crashed
// worker from clobbering a job another worker may have re-claimed.
func TestCompleteJobOnlyTransitionsRunning(t *testing.T) {
	ctx := context.Background()
	repo := openJobsTestRepo(t)

	// Happy path: a running job completes.
	j := enqueueAndClaim(t, repo)
	if err := repo.CompleteJob(ctx, j.ID, `{"ok":true}`); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got := jobStatus(t, repo, j.ID); got != repository.JobSucceeded {
		t.Fatalf("after complete: status=%q, want %q", got, repository.JobSucceeded)
	}

	// Guard path: claim, then reap back to pending (simulating a stuck worker),
	// then a late CompleteJob from the original worker must NOT transition it.
	j2 := enqueueAndClaim(t, repo)
	if n, err := repo.ReapStuckJobs(ctx, -time.Second); err != nil || n != 1 {
		t.Fatalf("reap: n=%d err=%v (want 1)", n, err)
	}
	if got := jobStatus(t, repo, j2.ID); got != repository.JobPending {
		t.Fatalf("after reap: status=%q, want %q", got, repository.JobPending)
	}
	if err := repo.CompleteJob(ctx, j2.ID, `{"ok":true}`); err != nil {
		t.Fatalf("late complete: %v", err)
	}
	if got := jobStatus(t, repo, j2.ID); got != repository.JobPending {
		t.Fatalf("late complete clobbered job: status=%q, want %q (no-op expected)", got, repository.JobPending)
	}
}

// TestFailJobOnlyTransitionsRunning mirrors the CompleteJob guard for FailJob.
func TestFailJobOnlyTransitionsRunning(t *testing.T) {
	ctx := context.Background()
	repo := openJobsTestRepo(t)

	// Happy path: a running job fails.
	j := enqueueAndClaim(t, repo)
	if err := repo.FailJob(ctx, j.ID, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if got := jobStatus(t, repo, j.ID); got != repository.JobFailed {
		t.Fatalf("after fail: status=%q, want %q", got, repository.JobFailed)
	}

	// Guard path: reaped-to-pending job must not be transitioned by a late FailJob.
	j2 := enqueueAndClaim(t, repo)
	if n, err := repo.ReapStuckJobs(ctx, -time.Second); err != nil || n != 1 {
		t.Fatalf("reap: n=%d err=%v (want 1)", n, err)
	}
	if err := repo.FailJob(ctx, j2.ID, "boom"); err != nil {
		t.Fatalf("late fail: %v", err)
	}
	if got := jobStatus(t, repo, j2.ID); got != repository.JobPending {
		t.Fatalf("late fail clobbered job: status=%q, want %q (no-op expected)", got, repository.JobPending)
	}
}

// TestRetryJobOnlyTransitionsRunning covers RetryJob's happy path: a running job
// retries back to pending with its error recorded.
func TestRetryJobOnlyTransitionsRunning(t *testing.T) {
	ctx := context.Background()
	repo := openJobsTestRepo(t)

	j := enqueueAndClaim(t, repo)
	if err := repo.RetryJob(ctx, j.ID, "transient", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := jobStatus(t, repo, j.ID); got != repository.JobPending {
		t.Fatalf("after retry: status=%q, want %q", got, repository.JobPending)
	}
}

func TestRetryJobRequeuesFailedWithFreshAttemptBudget(t *testing.T) {
	ctx := context.Background()
	repo := openJobsTestRepo(t)

	j := enqueueAndClaim(t, repo)
	if err := repo.FailJob(ctx, j.ID, "permanent"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if err := repo.RetryJob(ctx, j.ID, "manual retry", time.Now()); err != nil {
		t.Fatalf("manual retry: %v", err)
	}
	requeued := jobRecord(t, repo, j.ID)
	if requeued.Status != repository.JobPending || requeued.Attempts != 0 {
		t.Fatalf("requeued job: status=%q attempts=%d, want pending/0", requeued.Status, requeued.Attempts)
	}
	claimed, ok, err := repo.ClaimJob(ctx, "worker-B")
	if err != nil || !ok || claimed.ID != j.ID || claimed.Attempts != 1 {
		t.Fatalf("claim manual retry: job=%+v ok=%v err=%v", claimed, ok, err)
	}
}

// TestRetryJobNoOpWhilePending isolates the pending window: RetryJob against a
// job the reaper has put back to pending must affect zero rows, leaving the
// job's pending state and run_after untouched.
func TestRetryJobNoOpWhilePending(t *testing.T) {
	ctx := context.Background()
	repo := openJobsTestRepo(t)

	j := enqueueAndClaim(t, repo)
	if n, err := repo.ReapStuckJobs(ctx, -time.Second); err != nil || n != 1 {
		t.Fatalf("reap: n=%d err=%v (want 1)", n, err)
	}
	before := jobStatus(t, repo, j.ID)
	if before != repository.JobPending {
		t.Fatalf("after reap: status=%q, want %q", before, repository.JobPending)
	}

	// Late RetryJob from the original worker while the job sits pending: no-op.
	if err := repo.RetryJob(ctx, j.ID, "late", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("late retry: %v", err)
	}
	if got := jobStatus(t, repo, j.ID); got != repository.JobPending {
		t.Fatalf("late retry changed status: %q, want %q (no-op expected)", got, repository.JobPending)
	}

	// The pending job is still immediately claimable: the late RetryJob did not
	// push run_after an hour into the future (which it would have without the guard).
	if _, ok, err := repo.ClaimJob(ctx, "worker-B"); err != nil || !ok {
		t.Fatalf("re-claim after late retry: ok=%v err=%v (run_after was clobbered)", ok, err)
	}
}

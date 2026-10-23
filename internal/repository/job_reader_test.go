package repository

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openExactJobTestStore(t *testing.T) *sqlStore {
	t.Helper()
	repo, err := Open(t.Context(), "sqlite", "file:"+filepath.Join(t.TempDir(), "job-reader.db"))
	if err != nil {
		t.Fatalf("open job reader store: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate job reader store: %v", err)
	}
	store, ok := repo.(*sqlStore)
	if !ok {
		t.Fatalf("repo type %T, want *sqlStore", repo)
	}
	return store
}

func TestJobReaderReadsExactOldJob(t *testing.T) {
	ctx := t.Context()
	store := openExactJobTestStore(t)
	runAfter := time.Date(2025, 3, 4, 5, 6, 7, 8, time.UTC)
	oldID := enqueueJobForReader(t, store, Job{
		TenantID: "alpha", Type: "index_object", Payload: `{"object_id":17}`,
		Priority: 3, MaxAttempts: 4, DedupeKey: "old-key", RunAfter: runAfter,
	})
	newID := enqueueJobForReader(t, store, Job{TenantID: "beta", Type: "delete_chunks", Payload: `{"object_id":18}`})
	if newID <= oldID {
		t.Fatalf("new job id=%d, want id > old id %d", newID, oldID)
	}
	insertBulkJobs(t, store, 520)
	assertJobAbsentFromListCap(t, store, oldID)

	reader, err := NewJobReader(store)
	if err != nil {
		t.Fatalf("new job reader: %v", err)
	}
	lookup, err := reader.ReadJob(ctx, oldID)
	if err != nil {
		t.Fatalf("read old job: %v", err)
	}
	assertExactOldJob(t, lookup, oldID, runAfter)
	assertNewAndMissingJobLookups(t, reader, newID, oldID)
}

func enqueueJobForReader(t *testing.T, store *sqlStore, job Job) int64 {
	t.Helper()
	id, deduped, err := store.EnqueueJob(t.Context(), job)
	if err != nil || deduped || id <= 0 {
		t.Fatalf("enqueue job: id=%d deduped=%v err=%v", id, deduped, err)
	}
	return id
}

func assertJobAbsentFromListCap(t *testing.T, store *sqlStore, oldID int64) {
	t.Helper()
	jobs, err := store.ListJobs(t.Context(), "", "", 500)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 500 {
		t.Fatalf("listed jobs = %d, want the 500-row cap", len(jobs))
	}
	for _, job := range jobs {
		if job.ID == oldID {
			t.Fatalf("old job %d unexpectedly present in the capped list", oldID)
		}
	}
}

func assertExactOldJob(t *testing.T, lookup JobLookup, oldID int64, runAfter time.Time) {
	t.Helper()
	if !lookup.Present || lookup.Missing || lookup.RequestedID != oldID || lookup.Job.ID != oldID {
		t.Fatalf("old job lookup = %+v, want exact present id %d", lookup, oldID)
	}
	job := lookup.Job
	if job.TenantID != "alpha" || job.Type != "index_object" || job.Payload != `{"object_id":17}` {
		t.Fatalf("old job identity changed: %+v", job)
	}
	if job.Status != JobPending || job.Priority != 3 || job.Attempts != 0 || job.MaxAttempts != 4 {
		t.Fatalf("old job state changed: %+v", job)
	}
	if job.DedupeKey != "old-key" || !job.RunAfter.Equal(runAfter) {
		t.Fatalf("old job schedule/dedupe changed: %+v", job)
	}
	if job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() {
		t.Fatalf("old job timestamps missing: %+v", job)
	}
	if !job.StartedAt.IsZero() || !job.FinishedAt.IsZero() || job.Worker != "" || job.Result != "" || job.LastError != "" {
		t.Fatalf("pending job has unexpected execution fields: %+v", job)
	}
}

func assertNewAndMissingJobLookups(t *testing.T, reader JobReader, newID, oldID int64) {
	t.Helper()
	ctx := t.Context()
	newLookup, err := reader.ReadJob(ctx, newID)
	if err != nil || !newLookup.Present || newLookup.Job.ID != newID || newLookup.Job.Type != "delete_chunks" {
		t.Fatalf("new job lookup = %+v err=%v", newLookup, err)
	}
	missingID := oldID + 100000
	missing, err := reader.ReadJob(ctx, missingID)
	if err != nil {
		t.Fatalf("read missing job: %v", err)
	}
	if !missing.Missing || missing.Present || missing.RequestedID != missingID {
		t.Fatalf("missing lookup = %+v, want missing id %d", missing, missingID)
	}
	if missing.Job != (Job{}) {
		t.Fatalf("missing lookup leaked a job: %+v", missing.Job)
	}
}

func TestJobReaderInvalidRowsAreFaults(t *testing.T) {
	ctx := t.Context()
	store := openExactJobTestStore(t)
	reader, err := NewJobReader(store)
	if err != nil {
		t.Fatalf("new job reader: %v", err)
	}
	for _, column := range []string{"created_at", "run_after", "started_at"} {
		t.Run(column, func(t *testing.T) {
			id, _, err := store.EnqueueJob(ctx, Job{Type: "invalid-" + column})
			if err != nil {
				t.Fatalf("enqueue: %v", err)
			}
			result, err := store.db.ExecContext(ctx, store.rebind(
				`UPDATE jobs SET `+column+`=$1 WHERE id=$2`), "not-a-timestamp", id)
			if err != nil {
				t.Fatalf("corrupt row: %v", err)
			}
			if changed, err := result.RowsAffected(); err != nil || changed != 1 {
				t.Fatalf("corrupt row affected=%d err=%v, want 1", changed, err)
			}
			lookup, err := reader.ReadJob(ctx, id)
			if err == nil {
				t.Fatalf("invalid row %d scanned as %+v without an error", id, lookup)
			}
			if lookup.Present || lookup.Missing || lookup.RequestedID != 0 || lookup.Job != (Job{}) {
				t.Fatalf("invalid row returned partial lookup %+v", lookup)
			}
		})
	}
}

func TestJobReaderFailuresAreNotMissing(t *testing.T) {
	t.Run("canceled context", func(t *testing.T) {
		store := openExactJobTestStore(t)
		id, _, err := store.EnqueueJob(t.Context(), Job{Type: "context"})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		reader, err := NewJobReader(store)
		if err != nil {
			t.Fatalf("new job reader: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		lookup, err := reader.ReadJob(ctx, id)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("read with canceled context: lookup=%+v err=%v, want context.Canceled", lookup, err)
		}
		if lookup.Present || lookup.Missing {
			t.Fatalf("canceled read returned presence result %+v", lookup)
		}
	})

	t.Run("closed persistence", func(t *testing.T) {
		store := openExactJobTestStore(t)
		id, _, err := store.EnqueueJob(t.Context(), Job{Type: "closed"})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		reader, err := NewJobReader(store)
		if err != nil {
			t.Fatalf("new job reader: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
		lookup, err := reader.ReadJob(t.Context(), id)
		if err == nil {
			t.Fatalf("closed store returned %+v without an error", lookup)
		}
		if lookup.Present || lookup.Missing {
			t.Fatalf("closed store returned presence result %+v", lookup)
		}
	})
}

func TestNewJobReaderRejectsUnavailableStore(t *testing.T) {
	if reader, err := NewJobReader(nil); !errors.Is(err, ErrJobReaderUnavailable) || reader != nil {
		t.Fatalf("nil repository: reader=%v err=%v, want unavailable", reader, err)
	}
	var missing *sqlStore
	if reader, err := NewJobReader(missing); !errors.Is(err, ErrJobReaderUnavailable) || reader != nil {
		t.Fatalf("typed-nil store: reader=%v err=%v, want unavailable", reader, err)
	}
}

func insertBulkJobs(t *testing.T, store *sqlStore, count int) {
	t.Helper()
	ctx := t.Context()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin bulk jobs: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, store.rebind(
		`INSERT INTO jobs (tenant_id, type, payload, status, priority, attempts, max_attempts, run_after, last_error, worker, result, created_at, updated_at)
		 VALUES ($1,$2,$3,'pending',0,0,5,$4,'','','',$5,$6)`))
	if err != nil {
		t.Fatalf("prepare bulk jobs: %v", err)
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < count; i++ {
		if _, err := stmt.ExecContext(ctx, "bulk", "bulk-job", `{}`, now, now, now); err != nil {
			t.Fatalf("insert bulk job %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit bulk jobs: %v", err)
	}
}

package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openWebhookRepo(t *testing.T) (repository.Repository, context.Context) {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "wh.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo, ctx
}

func recordWebhook(t *testing.T, repo repository.Repository, ctx context.Context, eventID int64, url string, retryAt time.Time) int64 {
	t.Helper()
	id, err := repo.RecordWebhookFailure(ctx, repository.WebhookFailure{
		EventID:     eventID,
		URL:         url,
		Payload:     "{}",
		Attempts:    0,
		LastError:   "initial",
		LastStatus:  0,
		NextRetryAt: retryAt,
	})
	if err != nil {
		t.Fatalf("RecordWebhookFailure: %v", err)
	}
	return id
}

// TestNextPendingFailures verifies that NextPendingFailures returns only rows
// whose next_retry_at is due and that are still flagged not-succeeded, exercising
// the SQLite (`succeeded = 0`) query path that previously only ran as a masked
// fallback.
func TestNextPendingFailures(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "wf.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Due now: should be returned.
	dueID, err := repo.RecordWebhookFailure(ctx, repository.WebhookFailure{
		EventID:     1,
		URL:         "https://example.com/due",
		Payload:     "{}",
		Attempts:    1,
		LastError:   "boom",
		LastStatus:  500,
		NextRetryAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("record due: %v", err)
	}

	// Scheduled in the future: should be excluded.
	if _, err := repo.RecordWebhookFailure(ctx, repository.WebhookFailure{
		EventID:     2,
		URL:         "https://example.com/future",
		Payload:     "{}",
		Attempts:    1,
		LastError:   "boom",
		LastStatus:  500,
		NextRetryAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("record future: %v", err)
	}

	// Due now but already succeeded: should be excluded.
	succID, err := repo.RecordWebhookFailure(ctx, repository.WebhookFailure{
		EventID:     3,
		URL:         "https://example.com/done",
		Payload:     "{}",
		Attempts:    1,
		LastError:   "boom",
		LastStatus:  500,
		NextRetryAt: time.Now().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("record succeeded: %v", err)
	}
	if err := repo.MarkWebhookSucceeded(ctx, succID); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}

	got, err := repo.NextPendingFailures(ctx, 10)
	if err != nil {
		t.Fatalf("next pending: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 pending failure, got %d: %#v", len(got), got)
	}
	if got[0].ID != dueID {
		t.Fatalf("expected the due failure id=%d, got id=%d", dueID, got[0].ID)
	}
	if got[0].Succeeded {
		t.Fatalf("expected returned row to be not-succeeded, got %#v", got[0])
	}
}

func TestUpdateWebhookFailure(t *testing.T) {
	repo, ctx := openWebhookRepo(t)

	id := recordWebhook(t, repo, ctx, 1, "https://example.com/retry", time.Now().Add(-time.Minute))

	// Update the failure with new error info.
	if err := repo.UpdateWebhookFailure(ctx, id, "retry-error", 502,
		time.Now().Add(5*time.Minute), 2); err != nil {
		t.Fatalf("UpdateWebhookFailure: %v", err)
	}

	// Verify via ListWebhookFailures.
	all, err := repo.ListWebhookFailures(ctx, 10)
	if err != nil {
		t.Fatalf("ListWebhookFailures: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(all))
	}
	if all[0].LastError != "retry-error" || all[0].Attempts != 2 {
		t.Fatalf("expected LastError=retry-error Attempts=2, got %+v", all[0])
	}
}

func TestListWebhookFailures(t *testing.T) {
	repo, ctx := openWebhookRepo(t)

	// No failures yet.
	all, err := repo.ListWebhookFailures(ctx, 10)
	if err != nil {
		t.Fatalf("ListWebhookFailures empty: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected 0, got %d", len(all))
	}

	// Add two failures.
	id1 := recordWebhook(t, repo, ctx, 1, "https://a.com/1", time.Now())
	recordWebhook(t, repo, ctx, 2, "https://a.com/2", time.Now())
	_ = id1

	all, err = repo.ListWebhookFailures(ctx, 10)
	if err != nil {
		t.Fatalf("ListWebhookFailures after add: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}

	// Limit.
	limited, err := repo.ListWebhookFailures(ctx, 1)
	if err != nil {
		t.Fatalf("ListWebhookFailures limit: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("expected 1 with limit=1, got %d", len(limited))
	}
}

func TestMarkWebhookSucceeded(t *testing.T) {
	repo, ctx := openWebhookRepo(t)

	id := recordWebhook(t, repo, ctx, 1, "https://example.com/done", time.Now())

	// Mark as succeeded.
	if err := repo.MarkWebhookSucceeded(ctx, id); err != nil {
		t.Fatalf("MarkWebhookSucceeded: %v", err)
	}

	// Should no longer appear in pending.
	pending, err := repo.NextPendingFailures(ctx, 10)
	if err != nil {
		t.Fatalf("NextPendingFailures after mark: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending, got %d", len(pending))
	}
}

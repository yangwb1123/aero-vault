package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/storage"
)

// TestRetentionSweep_PurgesSoftDeleted verifies the retention GC permanently
// removes a soft-deleted object's row and its backing blob once it is older than
// the retention window. A negative retention pushes the cutoff into the future
// so the just-deleted row qualifies deterministically. (retention<=0 disables
// the sweep at the Run level — the safe production default — so the test
// exercises purgeSoftDeleted directly.)
func TestRetentionSweep_PurgesSoftDeleted(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant, bucket, key = "default", "default", "gone.txt"
	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	sk := "default/default/gone.txt"
	putBlob(t, store, sk)
	insertRow(t, repo, tenant, bucket, key, sk)
	if err := repo.SoftDeleteObject(ctx, tenant, bucket, key); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	// Negative retention → cutoff in the future → the just-deleted row qualifies.
	job := NewRetention(repo, store, time.Minute, -time.Hour, newSilentLogger())
	job.purgeSoftDeleted(ctx)

	// Row must be hard-gone: not retrievable and not listed as soft-deleted.
	if _, err := repo.GetObject(ctx, tenant, bucket, key); err == nil {
		t.Fatal("expected GetObject to fail after retention purge")
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	remaining, err := repo.ListSoftDeletedBefore(ctx, future, 10)
	if err != nil {
		t.Fatalf("list soft-deleted: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected no soft-deleted rows after purge, got %#v", remaining)
	}

	// Blob must be deleted from storage.
	if _, err := store.Stat(ctx, sk); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expected backing blob to be deleted (ErrNotFound), got %v", err)
	}
}

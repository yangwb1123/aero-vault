package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// TestListSoftDeletedBefore verifies that a soft-deleted object is returned when
// the `before` cutoff is in the future (it was deleted before "now+1h") and is
// excluded when the cutoff is in the far past (deleted_at >= cutoff).
func TestListSoftDeletedBefore(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "sdb.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const tenant, bucket, key = "default", "default", "gone.txt"
	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := repo.UpsertObject(ctx, repository.Object{
		TenantID:   tenant,
		Bucket:     bucket,
		Key:        key,
		StorageKey: "default/default/gone.txt",
		Backend:    "local",
		Size:       5,
		ETag:       "abc",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.SoftDeleteObject(ctx, tenant, bucket, key); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	got, err := repo.ListSoftDeletedBefore(ctx, future, 10)
	if err != nil {
		t.Fatalf("list (future): %v", err)
	}
	if len(got) != 1 || got[0].Key != key {
		t.Fatalf("expected the soft-deleted object, got %#v", got)
	}

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	empty, err := repo.ListSoftDeletedBefore(ctx, past, 10)
	if err != nil {
		t.Fatalf("list (past): %v", err)
	}
	if empty == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(empty) != 0 {
		t.Fatalf("expected no rows for a far-past cutoff, got %#v", empty)
	}
}

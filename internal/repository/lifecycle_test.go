package repository

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestSQLite(t *testing.T) *sqlStore {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "lc.db")
	repo, err := Open(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	s, ok := repo.(*sqlStore)
	if !ok {
		t.Fatalf("expected *sqlStore")
	}
	return s
}

// TestListExpiredCutoffAndSweep verifies the lifecycle selection query honours
// the per-bucket cutoff and that the sweep action (soft delete) removes the
// expired object while leaving fresh ones intact.
func TestListExpiredCutoffAndSweep(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t)
	const tenant, bucket = "default", "default"

	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := s.SetBucketLifecycle(ctx, tenant, bucket, 1, "soft_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}

	old, err := s.UpsertObject(ctx, Object{TenantID: tenant, Bucket: bucket, Key: "old.txt", Backend: "local", StorageKey: "k1", Size: 10, ETag: "e1"})
	if err != nil {
		t.Fatalf("upsert old: %v", err)
	}
	if _, err := s.UpsertObject(ctx, Object{TenantID: tenant, Bucket: bucket, Key: "new.txt", Backend: "local", StorageKey: "k2", Size: 10, ETag: "e2"}); err != nil {
		t.Fatalf("upsert new: %v", err)
	}

	// Backdate old.txt to 3 days ago so it falls past the 1-day cutoff.
	past := time.Now().UTC().Add(-72 * time.Hour).Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET updated_at=$1 WHERE id=$2`), past, old.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	expired, err := s.ListExpired(ctx, 100)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	if len(expired) != 1 || expired[0].Key != "old.txt" {
		t.Fatalf("expected only old.txt expired, got %+v", expired)
	}
	if expired[0].Metadata["__expire_action"] != "soft_delete" {
		t.Fatalf("expected __expire_action=soft_delete, got %q", expired[0].Metadata["__expire_action"])
	}

	// Apply the sweep action.
	if err := s.SoftDeleteObject(ctx, tenant, bucket, "old.txt"); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if _, err := s.GetObject(ctx, tenant, bucket, "old.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected old.txt gone, got err=%v", err)
	}
	if _, err := s.GetObject(ctx, tenant, bucket, "new.txt"); err != nil {
		t.Fatalf("expected new.txt to survive, got err=%v", err)
	}
	// Nothing left to expire.
	if again, _ := s.ListExpired(ctx, 100); len(again) != 0 {
		t.Fatalf("expected no expired after sweep, got %+v", again)
	}
}

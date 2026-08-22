package repository

import (
	"context"
	"testing"
	"time"
)

func TestListExpiredPaginatesPastFreshCandidates(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t)
	const tenant, bucket = "default", "expiry-page"

	if err := s.SetBucketLifecycle(ctx, tenant, bucket, 1, "soft_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}
	if _, err := s.UpsertObject(ctx, Object{TenantID: tenant, Bucket: bucket, Key: "fresh", Backend: "local", StorageKey: "fresh"}); err != nil {
		t.Fatalf("insert fresh object: %v", err)
	}
	old, err := s.UpsertObject(ctx, Object{TenantID: tenant, Bucket: bucket, Key: "old", Backend: "local", StorageKey: "old"})
	if err != nil {
		t.Fatalf("insert old object: %v", err)
	}
	backdateObject(t, s, ctx, old.ID, time.Now().UTC().Add(-72*time.Hour))

	got, err := s.ListExpired(ctx, 1)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	if len(got) != 1 || got[0].Key != "old" {
		t.Fatalf("expected old object past fresh candidate, got %+v", got)
	}
}

func TestListTransitionablePaginatesPastFreshCandidates(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t)
	const tenant, bucket = "default", "transition-page"

	if err := s.SetBucketLifecycleFull(ctx, tenant, bucket, LifecycleConfig{
		TransitionRules: []TransitionRule{{Days: 1, StorageClass: "GLACIER"}},
	}); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}
	if _, err := s.UpsertObject(ctx, Object{TenantID: tenant, Bucket: bucket, Key: "fresh", Backend: "local", StorageKey: "fresh"}); err != nil {
		t.Fatalf("insert fresh object: %v", err)
	}
	old, err := s.UpsertObject(ctx, Object{TenantID: tenant, Bucket: bucket, Key: "old", Backend: "local", StorageKey: "old"})
	if err != nil {
		t.Fatalf("insert old object: %v", err)
	}
	backdateObject(t, s, ctx, old.ID, time.Now().UTC().Add(-72*time.Hour))

	got, err := s.ListTransitionable(ctx, 1)
	if err != nil {
		t.Fatalf("list transitionable: %v", err)
	}
	if len(got) != 1 || got[0].Key != "old" {
		t.Fatalf("expected old object past fresh candidate, got %+v", got)
	}
	if got[0].Metadata["__transition_to"] != "GLACIER" {
		t.Fatalf("expected transition marker, got %v", got[0].Metadata)
	}
}

func TestListExpiredNonCurrentVersionsPaginatesPastFreshCandidates(t *testing.T) {
	ctx := context.Background()
	s := openTestSQLite(t)
	const tenant, bucket = "default", "noncurrent-page"

	if err := s.SetBucketNoncurrentVersionLifecycle(ctx, tenant, bucket, 1, 0); err != nil {
		t.Fatalf("set noncurrent lifecycle: %v", err)
	}
	fresh, err := s.UpsertObject(ctx, Object{TenantID: tenant, Bucket: bucket, Key: "fresh", Backend: "local", StorageKey: "fresh"})
	if err != nil {
		t.Fatalf("insert fresh object: %v", err)
	}
	if _, err := s.InsertDeleteMarker(ctx, fresh); err != nil {
		t.Fatalf("mark fresh object noncurrent: %v", err)
	}
	old, err := s.UpsertObject(ctx, Object{TenantID: tenant, Bucket: bucket, Key: "old", Backend: "local", StorageKey: "old"})
	if err != nil {
		t.Fatalf("insert old object: %v", err)
	}
	if _, err := s.InsertDeleteMarker(ctx, old); err != nil {
		t.Fatalf("mark old object noncurrent: %v", err)
	}
	backdateObject(t, s, ctx, old.ID, time.Now().UTC().Add(-72*time.Hour))

	got, err := s.ListExpiredNonCurrentVersions(ctx, 1)
	if err != nil {
		t.Fatalf("list expired noncurrent versions: %v", err)
	}
	if len(got) != 1 || got[0].ID != old.ID {
		t.Fatalf("expected old noncurrent version, got %+v", got)
	}
}

func backdateObject(t *testing.T, s *sqlStore, ctx context.Context, id int64, when time.Time) {
	t.Helper()
	stamp := when.UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET updated_at=$1, deleted_at=CASE WHEN deleted_at IS NULL THEN NULL ELSE $2 END WHERE id=$3`), stamp, stamp, id); err != nil {
		t.Fatalf("backdate object %d: %v", id, err)
	}
}

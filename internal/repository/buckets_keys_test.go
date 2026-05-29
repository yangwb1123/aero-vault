package repository_test

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func openTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

// TestListBuckets verifies that ListBuckets returns the buckets created for a
// tenant and never leaks buckets belonging to another tenant.
func TestListBuckets(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)

	if err := repo.CreateBucket(ctx, "tenantA", "alpha"); err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	if err := repo.CreateBucket(ctx, "tenantA", "beta"); err != nil {
		t.Fatalf("create beta: %v", err)
	}
	if err := repo.CreateBucket(ctx, "tenantB", "gamma"); err != nil {
		t.Fatalf("create gamma: %v", err)
	}

	got, err := repo.ListBuckets(ctx, "tenantA")
	if err != nil {
		t.Fatalf("list tenantA: %v", err)
	}
	sort.Strings(got)
	want := []string{"alpha", "beta"}
	if len(got) != len(want) {
		t.Fatalf("tenantA buckets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tenantA buckets = %v, want %v", got, want)
		}
	}

	// A tenant with no buckets gets an empty (non-nil) slice.
	none, err := repo.ListBuckets(ctx, "tenantC")
	if err != nil {
		t.Fatalf("list tenantC: %v", err)
	}
	if none == nil {
		t.Fatalf("expected non-nil empty slice for tenant with no buckets")
	}
	if len(none) != 0 {
		t.Fatalf("expected no buckets for tenantC, got %v", none)
	}
}

// TestStorageKeyReferenced verifies that a storage key is reported as
// referenced while a row exists, including after the object is soft-deleted,
// and is not reported for an unknown key.
func TestStorageKeyReferenced(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)

	const tenant, bucket, key, storageKey = "default", "default", "doc.txt", "blob/abc123"

	if _, err := repo.UpsertObject(ctx, repository.Object{
		TenantID:   tenant,
		Bucket:     bucket,
		Key:        key,
		StorageKey: storageKey,
		Backend:    "local",
		Size:       5,
		ETag:       "e1",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	ref, err := repo.StorageKeyReferenced(ctx, storageKey)
	if err != nil {
		t.Fatalf("referenced: %v", err)
	}
	if !ref {
		t.Fatalf("expected storage key %q to be referenced", storageKey)
	}

	unknown, err := repo.StorageKeyReferenced(ctx, "blob/does-not-exist")
	if err != nil {
		t.Fatalf("referenced unknown: %v", err)
	}
	if unknown {
		t.Fatalf("expected unknown storage key to be unreferenced")
	}

	// Soft-deleted objects still pin their blob.
	if err := repo.SoftDeleteObject(ctx, tenant, bucket, key); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	stillRef, err := repo.StorageKeyReferenced(ctx, storageKey)
	if err != nil {
		t.Fatalf("referenced after delete: %v", err)
	}
	if !stillRef {
		t.Fatalf("expected storage key %q to remain referenced after soft delete", storageKey)
	}
}

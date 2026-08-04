package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func TestLifecycleHardDeleteLegalHoldPreservesBlobAndRow(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)
	const tenant, bucket, key = "default", "default", "lifecycle-held.txt"

	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 1, "hard_delete"); err != nil {
		t.Fatal(err)
	}
	storageKey := "default/default/" + key
	putTestBlob(t, store, storageKey)
	obj := insertRow(t, h.repo, tenant, bucket, key, storageKey)
	if err := h.repo.PutLegalHold(ctx, repository.LegalHold{
		ObjectID: obj.ID, TenantID: tenant, VersionID: obj.VersionID,
	}); err != nil {
		t.Fatal(err)
	}
	h.backdateByID(t, obj.ID, 48*time.Hour)

	NewLifecycle(h.repo, store, time.Minute, newSilentLogger()).sweep(ctx)

	if _, err := h.repo.GetObject(ctx, tenant, bucket, key); err != nil {
		t.Fatalf("held row must remain: %v", err)
	}
	if _, err := store.Stat(ctx, storageKey); err != nil {
		t.Fatalf("held blob must remain: %v", err)
	}
}

func TestRetentionLegalHoldPreservesBlobAndSoftDeletedRow(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)
	const tenant, bucket, key = "default", "default", "retention-held.txt"

	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatal(err)
	}
	storageKey := "default/default/" + key
	putBlob(t, store, storageKey)
	obj := insertRow(t, repo, tenant, bucket, key, storageKey)
	if err := repo.PutLegalHold(ctx, repository.LegalHold{
		ObjectID: obj.ID, TenantID: tenant, VersionID: obj.VersionID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SoftDeleteObject(ctx, tenant, bucket, key); err != nil {
		t.Fatal(err)
	}

	NewRetention(repo, store, time.Minute, -time.Hour, newSilentLogger()).purgeSoftDeleted(ctx)

	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	remaining, err := repo.ListSoftDeletedBefore(ctx, future, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != obj.ID {
		t.Fatalf("held soft-deleted row must remain, got %#v", remaining)
	}
	if _, err := store.Stat(ctx, storageKey); err != nil {
		t.Fatalf("held blob must remain: %v", err)
	}
}

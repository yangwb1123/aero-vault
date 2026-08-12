package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func TestHardDeleteKeyRemovesEveryVersionAndAdjustsUsage(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocal(storage.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllAuthz{})

	if err := svc.SetBucketVersioning(ctx, "default", "versions", true); err != nil {
		t.Fatal(err)
	}
	v1, err := svc.Put(ctx, "default", "versions", "file.txt", strings.NewReader("one"), 3, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := svc.Put(ctx, "default", "versions", "file.txt", strings.NewReader("two-two"), 7, service.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "default", "versions", "file.txt", false); err != nil {
		t.Fatal(err)
	}
	versions, err := repo.ListObjectVersions(ctx, "default", "versions", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	var deletedCurrent repository.Object
	for _, version := range versions {
		if !version.VersionTombstone {
			deletedCurrent = version
		}
	}
	if deletedCurrent.ID == 0 {
		t.Fatal("soft-deleted current version not found")
	}
	if err := hardDeleteKey(ctx, repo, store, nil, deletedCurrent, slog.Default()); err != nil {
		t.Fatal(err)
	}
	versions, err = repo.ListObjectVersions(ctx, "default", "versions", "file.txt")
	if err != nil || len(versions) != 0 {
		t.Fatalf("versions after purge=%+v err=%v", versions, err)
	}
	for _, key := range []string{v1.StorageKey, v2.StorageKey} {
		if _, err := store.Stat(ctx, key); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("storage key %q survived purge: %v", key, err)
		}
	}
	quota, err := repo.GetTenantQuota(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	if quota.UsedBytes != 0 || quota.UsedObjects != 0 {
		t.Fatalf("usage after purge=%d bytes/%d objects", quota.UsedBytes, quota.UsedObjects)
	}
}

// allowAllAuthz is the CI-baseline test double injected into bare FileService
// constructions that exercise deletion: it preserves the pre-fail-closed
// baseline (all actions allowed). The fail-closed delete gate is covered by
// dedicated tests in internal/service.
// ---- AC-1: protection must be re-checked at delete time (TOCTOU gap) ----
//
// The three tests below reproduce the gap deterministically: the caller's
// pre-check runs first (pure read, no side effects), then the hold/WORM lock
// lands, then hardDeleteKey runs. On the unfixed tree hardDeleteKey destroys
// blobs before the DB gate; with the fix it must skip with zero side effects.

// TestHardDeleteKey_LegalHoldAfterPrecheck_PreservesBlobAndRow is AC-1 T-1:
// a legal hold placed after the protection pre-check (but before hardDeleteKey)
// leaves every version's blob present in storage and the DB row intact.
func TestHardDeleteKey_LegalHoldAfterPrecheck_PreservesBlobAndRow(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)
	const tenant, bucket, key = "default", "default", "race-hold.txt"
	storageKey := "default/default/" + key
	putTestBlob(t, store, storageKey)
	obj := insertRow(t, h.repo, tenant, bucket, key, storageKey)

	// Simulate the sweep's protection pre-check: it passes (no hold yet).
	protected, err := objectKeyDeletionProtected(ctx, h.repo, obj)
	if err != nil || protected {
		t.Fatalf("pre-check must pass before hold: protected=%v err=%v", protected, err)
	}
	// The legal hold lands after the pre-check, before hardDeleteKey.
	if err := h.repo.PutLegalHold(ctx, repository.LegalHold{
		ObjectID: obj.ID, TenantID: tenant, VersionID: obj.VersionID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := hardDeleteKey(ctx, h.repo, store, nil, obj, newSilentLogger()); !errors.Is(err, errKeyProtected) {
		t.Fatalf("hardDeleteKey must skip with errKeyProtected, got %v", err)
	}
	// Blob intact.
	if _, err := store.Stat(ctx, storageKey); err != nil {
		t.Fatalf("held blob must remain: %v", err)
	}
	// Row intact.
	versions, err := h.repo.ListObjectVersions(ctx, tenant, bucket, key)
	if err != nil || len(versions) != 1 {
		t.Fatalf("row must remain: versions=%+v err=%v", versions, err)
	}
	held, err := h.repo.ObjectHasLegalHold(ctx, obj.ID)
	if err != nil || !held {
		t.Fatalf("legal hold must still be recorded: held=%v err=%v", held, err)
	}
	if _, err := h.repo.GetObject(ctx, tenant, bucket, key); err != nil {
		t.Fatalf("row must be readable: %v", err)
	}
	// FR-1.3 regression guard: the skip must NOT be counted as hard-deleted.
	if done := NewLifecycle(h.repo, store, time.Minute, newSilentLogger()).
		handleExpiredObject(ctx, obj, "hard_delete"); done {
		t.Fatal("protected object must not be counted as hard-deleted")
	}
}

// TestHardDeleteKey_WORMLockAfterPrecheck_PreservesBlobAndRow is AC-1 T-1b:
// same race with a WORM lock (locked_until). The repository-level DB gate
// never checks locked_until, so this is the severest path: without the
// protection.go re-check both row and blob disappear.
func TestHardDeleteKey_WORMLockAfterPrecheck_PreservesBlobAndRow(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)
	const tenant, bucket, key = "default", "default", "race-worm.txt"
	storageKey := "default/default/" + key
	putTestBlob(t, store, storageKey)
	obj := insertRow(t, h.repo, tenant, bucket, key, storageKey)

	protected, err := objectKeyDeletionProtected(ctx, h.repo, obj)
	if err != nil || protected {
		t.Fatalf("pre-check must pass before lock: protected=%v err=%v", protected, err)
	}
	if err := h.repo.SetLockedUntil(ctx, tenant, bucket, key, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := hardDeleteKey(ctx, h.repo, store, nil, obj, newSilentLogger()); !errors.Is(err, errKeyProtected) {
		t.Fatalf("hardDeleteKey must skip with errKeyProtected, got %v", err)
	}
	if _, err := store.Stat(ctx, storageKey); err != nil {
		t.Fatalf("locked blob must remain: %v", err)
	}
	if _, err := h.repo.GetObject(ctx, tenant, bucket, key); err != nil {
		t.Fatalf("locked row must remain: %v", err)
	}
}

// TestHardDeleteKey_MultiVersion_HoldOnCurrent_PreservesAllBlobs is AC-1
// T-1c: on a multi-version key a hold on the current version must leave every
// version's blob and row intact ("every version's blob present in storage").
func TestHardDeleteKey_MultiVersion_HoldOnCurrent_PreservesAllBlobs(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)
	const tenant, bucket, key = "default", "default", "race-multi.txt"
	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetBucketVersioning(ctx, tenant, bucket, true); err != nil {
		t.Fatal(err)
	}
	k1 := "default/default/" + key + "@v1"
	k2 := "default/default/" + key + "@v2"
	if _, err := h.repo.InsertObjectVersion(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: key, VersionID: "v1",
		Backend: "local", StorageKey: k1, Size: 5, ETag: "e1",
	}); err != nil {
		t.Fatal(err)
	}
	putTestBlob(t, store, k1)
	v2, err := h.repo.InsertObjectVersion(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: key, VersionID: "v2",
		Backend: "local", StorageKey: k2, Size: 5, ETag: "e2",
	})
	if err != nil {
		t.Fatal(err)
	}
	putTestBlob(t, store, k2)
	// v1 is now a tombstone (deleted_at set, version_tombstone=1).

	protected, err := objectKeyDeletionProtected(ctx, h.repo, v2)
	if err != nil || protected {
		t.Fatalf("pre-check must pass before hold: protected=%v err=%v", protected, err)
	}
	if err := h.repo.PutLegalHold(ctx, repository.LegalHold{
		ObjectID: v2.ID, TenantID: tenant, VersionID: v2.VersionID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := hardDeleteKey(ctx, h.repo, store, nil, v2, newSilentLogger()); !errors.Is(err, errKeyProtected) {
		t.Fatalf("hardDeleteKey must skip with errKeyProtected, got %v", err)
	}
	for _, k := range []string{k1, k2} {
		if _, err := store.Stat(ctx, k); err != nil {
			t.Fatalf("blob %q must remain under hold: %v", k, err)
		}
	}
	versions, err := h.repo.ListObjectVersions(ctx, tenant, bucket, key)
	if err != nil || len(versions) != 2 {
		t.Fatalf("both rows must remain: versions=%+v err=%v", versions, err)
	}
}

type allowAllAuthz struct{}

func (allowAllAuthz) Authorize(context.Context, access.Principal, access.Action, access.Resource) (access.Decision, error) {
	return access.Decision{Allowed: true, Reason: "test_allow_all"}, nil
}

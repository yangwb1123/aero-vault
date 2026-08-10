package reconcile

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// openTestRepo creates a fresh in-memory SQLite repository ready for use.
func openTestRepo(t *testing.T) repository.Repository {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

// openTestStore creates a fresh local storage backend.
func openTestStore(t *testing.T) *storage.LocalStorage {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	if err != nil {
		t.Fatalf("new local storage: %v", err)
	}
	return store
}

// putBlob writes a small blob directly to the storage layer (no DB row).
func putBlob(t *testing.T, store storage.Storage, key string) {
	t.Helper()
	_, err := store.Put(context.Background(), key, strings.NewReader("hello"), 5, storage.PutOptions{})
	if err != nil {
		t.Fatalf("put blob %q: %v", key, err)
	}
}

// insertRow inserts an object row into the repository without writing any blob.
func insertRow(t *testing.T, repo repository.Repository, tenant, bucket, key, storageKey string) repository.Object {
	t.Helper()
	obj, err := repo.UpsertObject(context.Background(), repository.Object{
		TenantID:   tenant,
		Bucket:     bucket,
		Key:        key,
		StorageKey: storageKey,
		Backend:    "local",
		Size:       5,
		ETag:       "abc",
	})
	if err != nil {
		t.Fatalf("upsert row %q: %v", key, err)
	}
	return obj
}

// newSilentLogger returns a logger that discards output so test output stays clean.
func newSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------- Job (orphan sweep) tests ----------

// TestJobSweep_OrphanRow_Detected verifies that an object whose DB row exists
// but whose blob is absent in storage is detected and soft-deleted by sweep.
func TestJobSweep_OrphanRow_Detected(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"

	// Ensure the bucket row exists so ListObjects works.
	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// Insert a DB row for a blob that does NOT exist in storage.
	insertRow(t, repo, tenant, bucket, "orphan-key.txt", "default/default/orphan-key.txt")

	// Confirm it is retrievable before the sweep.
	if _, err := repo.GetObject(ctx, tenant, bucket, "orphan-key.txt"); err != nil {
		t.Fatalf("expected row before sweep: %v", err)
	}

	job := New(repo, store, time.Minute, false, time.Minute, []string{tenant}, newSilentLogger())
	job.sweep(ctx)

	// After sweep the orphan row must be soft-deleted (ErrNotFound).
	if _, err := repo.GetObject(ctx, tenant, bucket, "orphan-key.txt"); err == nil {
		t.Fatal("expected orphan row to be soft-deleted after sweep, but GetObject succeeded")
	}
}

// TestJobSweep_HealthyObject_Preserved verifies that an object with both a DB
// row and a matching blob survives the sweep untouched.
func TestJobSweep_HealthyObject_Preserved(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"

	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	sk := "default/default/healthy.txt"
	putBlob(t, store, sk)
	insertRow(t, repo, tenant, bucket, "healthy.txt", sk)

	job := New(repo, store, time.Minute, false, time.Minute, []string{tenant}, newSilentLogger())
	job.sweep(ctx)

	// Object must still be retrievable.
	if _, err := repo.GetObject(ctx, tenant, bucket, "healthy.txt"); err != nil {
		t.Fatalf("healthy object was removed by sweep: %v", err)
	}
}

// TestJobSweep_MultipleObjects_OnlyOrphansRemoved verifies that a mix of
// healthy and orphan rows is handled correctly: only orphan rows are removed.
func TestJobSweep_MultipleObjects_OnlyOrphansRemoved(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"
	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// Insert two orphan rows (no blobs).
	insertRow(t, repo, tenant, bucket, "orphan1.txt", "default/default/orphan1.txt")
	insertRow(t, repo, tenant, bucket, "orphan2.txt", "default/default/orphan2.txt")

	// Insert one healthy object (blob + row).
	sk := "default/default/good.txt"
	putBlob(t, store, sk)
	insertRow(t, repo, tenant, bucket, "good.txt", sk)

	job := New(repo, store, time.Minute, false, time.Minute, []string{tenant}, newSilentLogger())
	job.sweep(ctx)

	if _, err := repo.GetObject(ctx, tenant, bucket, "orphan1.txt"); err == nil {
		t.Fatal("orphan1.txt should have been soft-deleted")
	}
	if _, err := repo.GetObject(ctx, tenant, bucket, "orphan2.txt"); err == nil {
		t.Fatal("orphan2.txt should have been soft-deleted")
	}
	if _, err := repo.GetObject(ctx, tenant, bucket, "good.txt"); err != nil {
		t.Fatalf("good.txt should still exist: %v", err)
	}
}

// TestJobSweep_OrphanBlob_LeftInPlaceByDefault verifies that a blob in storage
// with no corresponding DB row is detected but NOT deleted when
// deleteOrphanBlobs is false (the safe default): the sweep logs/counts it but
// leaves it in place.
func TestJobSweep_OrphanBlob_LeftInPlaceByDefault(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"
	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// Blob in storage with NO DB row.
	orphanBlobKey := "default/default/blob-only.bin"
	putBlob(t, store, orphanBlobKey)

	job := New(repo, store, time.Minute, false, time.Minute, []string{tenant}, newSilentLogger())
	job.sweep(ctx)

	// Blob must still be present in storage (not deleted).
	if _, err := store.Stat(ctx, orphanBlobKey); err != nil {
		t.Fatalf("orphan blob was unexpectedly deleted by sweep: %v", err)
	}
}

// TestJobSweep_EmptyBucket_NoError verifies that sweeping a tenant with no
// objects completes without error.
func TestJobSweep_EmptyBucket_NoError(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant = "default"
	if err := repo.CreateBucket(ctx, tenant, "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	job := New(repo, store, time.Minute, false, time.Minute, []string{tenant}, newSilentLogger())
	// Should not panic or return error.
	job.sweep(ctx)
}

// TestJobSweep_MultipleTenants verifies that the sweep iterates over all
// tenants passed to New.
func TestJobSweep_MultipleTenants(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	tenants := []string{"tenantA", "tenantB"}
	for _, t2 := range tenants {
		if err := repo.CreateBucket(ctx, t2, "default"); err != nil {
			t.Fatalf("create bucket for %s: %v", t2, err)
		}
		// Insert an orphan row for each tenant.
		insertRow(t, repo, t2, "default", "missing.txt", t2+"/default/missing.txt")
	}

	job := New(repo, store, time.Minute, false, time.Minute, tenants, newSilentLogger())
	job.sweep(ctx)

	for _, t2 := range tenants {
		if _, err := repo.GetObject(ctx, t2, "default", "missing.txt"); err == nil {
			t.Errorf("expected orphan row for tenant %s to be soft-deleted", t2)
		}
	}
}

// TestJob_New_DefaultTenant verifies that New defaults to ["default"] when no
// tenants are provided.
func TestJob_New_DefaultTenant(t *testing.T) {
	repo := openTestRepo(t)
	store := openTestStore(t)

	j := New(repo, store, time.Minute, false, time.Minute, nil, nil)
	if len(j.tenants) != 1 || j.tenants[0] != "default" {
		t.Fatalf("expected tenants=[\"default\"], got %v", j.tenants)
	}
}

// TestJob_New_NilLogger verifies that New substitutes slog.Default() when nil
// logger is passed.
func TestJob_New_NilLogger(t *testing.T) {
	repo := openTestRepo(t)
	store := openTestStore(t)

	j := New(repo, store, time.Minute, false, time.Minute, nil, nil)
	if j.logger == nil {
		t.Fatal("expected non-nil logger after New with nil")
	}
}

// TestJob_Run_ZeroInterval verifies that Run returns immediately when interval
// is zero (disabled mode).
func TestJob_Run_ZeroInterval(t *testing.T) {
	repo := openTestRepo(t)
	store := openTestStore(t)

	j := New(repo, store, 0, false, time.Minute, nil, newSilentLogger())

	done := make(chan struct{})
	go func() {
		ctx := context.Background()
		j.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("Run with zero interval did not return promptly")
	}
}

// TestJob_Run_CancelledContext verifies that Run exits when context is
// cancelled (smoke test; main coverage from direct sweep calls).
func TestJob_Run_CancelledContext(t *testing.T) {
	repo := openTestRepo(t)
	store := openTestStore(t)

	j := New(repo, store, 10*time.Millisecond, false, time.Minute, nil, newSilentLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	done := make(chan struct{})
	go func() {
		j.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

// TestJobSweep_PaginationHandled verifies that sweep correctly handles
// paginated object lists (objects > 200 per page).
func TestJobSweep_PaginationHandled(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"
	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// Insert 250 objects - some healthy, some orphan rows.
	// We'll make 50 orphans and 200 healthy to span the 200-object page boundary.
	const total = 250
	const orphanCount = 50

	healthyKeys := make([]string, 0, total-orphanCount)
	for i := 0; i < total; i++ {
		key := "obj-" + padInt(i) + ".txt"
		sk := "default/default/" + key
		if i < orphanCount {
			// orphan row: no blob
			insertRow(t, repo, tenant, bucket, key, sk)
		} else {
			putBlob(t, store, sk)
			insertRow(t, repo, tenant, bucket, key, sk)
			healthyKeys = append(healthyKeys, key)
		}
	}

	job := New(repo, store, time.Minute, false, time.Minute, []string{tenant}, newSilentLogger())
	job.sweep(ctx)

	// Orphan rows removed.
	for i := 0; i < orphanCount; i++ {
		key := "obj-" + padInt(i) + ".txt"
		if _, err := repo.GetObject(ctx, tenant, bucket, key); err == nil {
			t.Errorf("orphan row %s should have been soft-deleted", key)
		}
	}

	// Healthy rows survive.
	for _, key := range healthyKeys {
		if _, err := repo.GetObject(ctx, tenant, bucket, key); err != nil {
			t.Errorf("healthy object %s should still exist: %v", key, err)
		}
	}
}

// padInt returns a zero-padded 3-digit string for an integer.
func padInt(n int) string {
	s := "000" + itoa(n)
	return s[len(s)-3:]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 10)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestJobSweep_ClusterSingleton_OnlyOneRuns verifies that when two instances
// share a repository and both run as cluster singletons, only the lease holder
// performs the sweep.
func TestJobSweep_ClusterSingleton_OnlyOneRuns(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"
	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	jA := New(repo, store, time.Minute, false, time.Minute, []string{tenant}, newSilentLogger()).WithClusterSingleton("node-A")
	jB := New(repo, store, time.Minute, false, time.Minute, []string{tenant}, newSilentLogger()).WithClusterSingleton("node-B")

	// node-A acquires the lease and sweeps the first orphan row.
	insertRow(t, repo, tenant, bucket, "first.txt", "default/default/first.txt")
	jA.maybeSweep(ctx)
	if _, err := repo.GetObject(ctx, tenant, bucket, "first.txt"); err == nil {
		t.Fatal("node-A (lease holder) should have swept the first orphan")
	}

	// node-B must NOT sweep while node-A's lease is still valid.
	insertRow(t, repo, tenant, bucket, "second.txt", "default/default/second.txt")
	jB.maybeSweep(ctx)
	if _, err := repo.GetObject(ctx, tenant, bucket, "second.txt"); err != nil {
		t.Fatal("node-B must not sweep while node-A holds the lease; the second orphan should remain")
	}

	// node-A renews its lease and sweeps the second orphan on its next run.
	jA.maybeSweep(ctx)
	if _, err := repo.GetObject(ctx, tenant, bucket, "second.txt"); err == nil {
		t.Fatal("node-A should have swept the second orphan on its next run")
	}
}

// ---------- Orphan-blob direction tests ----------

// TestJobSweep_OrphanBlob_DeletedWhenEnabled verifies that an unreferenced blob
// is removed when deleteOrphanBlobs is true and it is older than the grace
// period (grace=0 makes any age eligible).
func TestJobSweep_OrphanBlob_DeletedWhenEnabled(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant = "default"
	if err := repo.CreateBucket(ctx, tenant, "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	orphanBlobKey := "default/default/blob-only.bin"
	putBlob(t, store, orphanBlobKey)

	// deleteOrphanBlobs=true, gracePeriod=0 → eligible regardless of age.
	job := New(repo, store, time.Minute, true, 0, []string{tenant}, newSilentLogger())
	job.sweep(ctx)

	if _, err := store.Stat(ctx, orphanBlobKey); err == nil {
		t.Fatal("orphan blob should have been deleted when cleanup is enabled")
	}
}

// TestJobSweep_OrphanBlob_GracePeriodProtectsFresh verifies that a freshly
// written, unreferenced blob is NOT deleted while within the grace period —
// it may simply be an upload whose DB row has not yet committed.
func TestJobSweep_OrphanBlob_GracePeriodProtectsFresh(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant = "default"
	if err := repo.CreateBucket(ctx, tenant, "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	freshKey := "default/default/just-uploaded.bin"
	putBlob(t, store, freshKey)

	// Cleanup enabled, but a large grace period protects the just-written blob.
	job := New(repo, store, time.Minute, true, time.Hour, []string{tenant}, newSilentLogger())
	job.sweep(ctx)

	if _, err := store.Stat(ctx, freshKey); err != nil {
		t.Fatalf("fresh blob inside grace period must not be deleted: %v", err)
	}
}

// TestJobSweep_VersionedBlob_NeverDeleted is the data-loss regression guard:
// versioned objects use a per-version storage key ("…@v<id>"). A blob whose
// exact storage_key is referenced by any row must survive, even with cleanup
// enabled and zero grace.
func TestJobSweep_VersionedBlob_NeverDeleted(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"
	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// A non-current version: distinct storage key with the @v suffix, still
	// referenced by a row.
	versionKey := "default/default/doc.txt@v1700000000000000000"
	putBlob(t, store, versionKey)
	insertRow(t, repo, tenant, bucket, "doc.txt", versionKey)

	job := New(repo, store, time.Minute, true, 0, []string{tenant}, newSilentLogger())
	job.sweep(ctx)

	if _, err := store.Stat(ctx, versionKey); err != nil {
		t.Fatalf("referenced version blob must never be deleted: %v", err)
	}
}

// TestJobSweep_SoftDeletedBlob_Survives verifies that the blob of a
// soft-deleted object is preserved (soft delete is restorable, so the existence
// check must ignore deleted_at).
func TestJobSweep_SoftDeletedBlob_Survives(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"
	if err := repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	sk := "default/default/recoverable.txt"
	putBlob(t, store, sk)
	insertRow(t, repo, tenant, bucket, "recoverable.txt", sk)
	if err := repo.SoftDeleteObject(ctx, tenant, bucket, "recoverable.txt"); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	job := New(repo, store, time.Minute, true, 0, []string{tenant}, newSilentLogger())
	job.sweep(ctx)

	if _, err := store.Stat(ctx, sk); err != nil {
		t.Fatalf("soft-deleted object's blob must survive (restorable): %v", err)
	}
}

// TestJobSweep_OrphanRow_NonDefaultBucket is the bucket-scope regression guard:
// an orphan row in a bucket other than "default" must now be reconciled (the
// sweep previously hardcoded the "default" bucket).
func TestJobSweep_OrphanRow_NonDefaultBucket(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant = "default"
	if err := repo.CreateBucket(ctx, tenant, "photos"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	// Orphan row (no blob) in a non-default bucket.
	insertRow(t, repo, tenant, "photos", "missing.jpg", "default/photos/missing.jpg")

	job := New(repo, store, time.Minute, false, time.Minute, []string{tenant}, newSilentLogger())
	job.sweep(ctx)

	if _, err := repo.GetObject(ctx, tenant, "photos", "missing.jpg"); err == nil {
		t.Fatal("orphan row in non-default bucket should have been soft-deleted")
	}
}

// TestJobSweep_OrphanBlob_Idempotent verifies that re-running the sweep over the
// same state is safe (deleting an already-deleted blob is a no-op per the
// Storage contract).
func TestJobSweep_OrphanBlob_Idempotent(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store := openTestStore(t)

	const tenant = "default"
	if err := repo.CreateBucket(ctx, tenant, "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	orphanBlobKey := "default/default/blob-only.bin"
	putBlob(t, store, orphanBlobKey)

	job := New(repo, store, time.Minute, true, 0, []string{tenant}, newSilentLogger())
	job.sweep(ctx) // first sweep deletes it
	job.sweep(ctx) // second sweep must be a harmless no-op

	if _, err := store.Stat(ctx, orphanBlobKey); err == nil {
		t.Fatal("orphan blob should remain deleted after idempotent re-run")
	}
}

// TestJobSweep_ReportsScrubCorruptCount verifies that the sweep summary log
// carries the scrub_corrupt field (previously the corrupt count was dropped).
// Sub-scenarios: A = one corrupt object → scrub_corrupt=1; B = healthy control
// → scrub_corrupt=0 (field always present); C = two tenants with one corrupt
// object each → cumulative scrub_corrupt=2 (regression guard against a loop-
// scoped `:=` shadowing the accumulator and reporting only the last tenant).
func TestJobSweep_ReportsScrubCorruptCount(t *testing.T) {
	ctx := context.Background()

	// 子场景 A：1 个损坏对象 → scrub_corrupt=1
	repo := openTestRepo(t)
	store := openTestStore(t)
	if err := repo.CreateBucket(ctx, "default", "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	fileService := service.NewFileService(store, repo, nil).WithAuthorizer(allowAllAuthz{})
	body := "trusted content"
	tampered := "altered content"
	digest := md5.Sum([]byte(body))
	object, err := fileService.Put(ctx, "default", "default", "corrupt.txt",
		strings.NewReader(body), int64(len(body)),
		service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(digest[:])})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := store.Put(ctx, object.StorageKey, strings.NewReader(tampered), int64(len(tampered)), storage.PutOptions{}); err != nil {
		t.Fatalf("tamper storage: %v", err)
	}
	var bufA bytes.Buffer
	jobA := New(repo, store, time.Minute, false, time.Minute, []string{"default"},
		slog.New(slog.NewTextHandler(&bufA, nil))).WithScrub(true, 100).
		WithChunkCleaner(repositoryChunkCleaner{repo: repo})
	jobA.sweep(ctx)
	logA := bufA.String()
	if !strings.Contains(logA, "reconcile sweep done") || !strings.Contains(logA, "scrub_corrupt=1") {
		t.Fatalf("sweep log missing scrub_corrupt=1: %s", logA)
	}

	// 子场景 B：健康对象对照 → scrub_corrupt=0（字段恒携带）
	repo2 := openTestRepo(t)
	store2 := openTestStore(t)
	if err := repo2.CreateBucket(ctx, "default", "default"); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	fileService2 := service.NewFileService(store2, repo2, nil).WithAuthorizer(allowAllAuthz{})
	digest2 := md5.Sum([]byte(body))
	if _, err := fileService2.Put(ctx, "default", "default", "healthy.txt",
		strings.NewReader(body), int64(len(body)),
		service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(digest2[:])}); err != nil {
		t.Fatalf("put healthy: %v", err)
	}
	var bufB bytes.Buffer
	jobB := New(repo2, store2, time.Minute, false, time.Minute, []string{"default"},
		slog.New(slog.NewTextHandler(&bufB, nil))).WithScrub(true, 100)
	jobB.sweep(ctx)
	logB := bufB.String()
	if !strings.Contains(logB, "reconcile sweep done") || !strings.Contains(logB, "scrub_corrupt=0") {
		t.Fatalf("sweep log missing scrub_corrupt=0: %s", logB)
	}

	// 子场景 C（D3 回归护栏）：双租户各 1 损坏对象 → 累计 scrub_corrupt=2
	repo3 := openTestRepo(t)
	store3 := openTestStore(t)
	tenants := []string{"tenantA", "tenantB"}
	for _, t2 := range tenants {
		if err := repo3.CreateBucket(ctx, t2, "default"); err != nil {
			t.Fatalf("create bucket %s: %v", t2, err)
		}
		fs := service.NewFileService(store3, repo3, nil).WithAuthorizer(allowAllAuthz{})
		d := md5.Sum([]byte(body))
		obj, err := fs.Put(ctx, t2, "default", "corrupt.txt",
			strings.NewReader(body), int64(len(body)),
			service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(d[:])})
		if err != nil {
			t.Fatalf("put %s: %v", t2, err)
		}
		if _, err := store3.Put(ctx, obj.StorageKey, strings.NewReader(tampered), int64(len(tampered)), storage.PutOptions{}); err != nil {
			t.Fatalf("tamper %s: %v", t2, err)
		}
	}
	var bufC bytes.Buffer
	jobC := New(repo3, store3, time.Minute, false, time.Minute, tenants,
		slog.New(slog.NewTextHandler(&bufC, nil))).WithScrub(true, 100)
	jobC.sweep(ctx)
	logC := bufC.String()
	if !strings.Contains(logC, "scrub_corrupt=2") {
		t.Fatalf("sweep log missing cumulative scrub_corrupt=2: %s", logC)
	}
}

package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// backdateObject reaches into the underlying SQLite DB via repository to
// force the updated_at timestamp of an object to be in the past.
// We use direct SQL because the repository interface has no explicit
// "set updated_at" method.
func backdateObject(t *testing.T, repo repository.Repository, id int64, age time.Duration) {
	t.Helper()
	// Access the internal *sqlStore to run raw SQL.
	type rawDB interface {
		DB() *sql.DB
	}
	// We rely on the fact that the test repo is a *sqlStore and exposes
	// internal access. Since we're in-package for reconcile (not repository),
	// we can't directly access sqlStore. Instead we use a helper query via
	// the exported Migrate path and leverage the fact that we can get at the
	// db through a special interface.
	//
	// Actually, the cleanest approach without importing internals is to use
	// OpenTestSQLite via the repository package which is not exported.
	// We must rely on a different approach: since repository.Object has UpdatedAt
	// but no way to set it via the public API, we use repository.Open to
	// open the SAME database file and cast to the internal type.
	//
	// However, since we are in package `reconcile` (not `repository`),
	// we cannot access *sqlStore directly.
	//
	// Workaround: use a shared test helper that writes via raw SQL.
	// Since repository.Open always returns *sqlStore for sqlite, and we can
	// use a raw sql.Open to the same file, we do the backdating via
	// the same DSN stored in a package-level test var.
	_ = id
	_ = age
	t.Fatal("backdateObject: internal helper not reachable from reconcile package; use testBackdate instead")
}

// testRepoWithDB wraps a repository and its raw sql.DB for backdating.
type testRepoWithDB struct {
	repo repository.Repository
	db   *sql.DB
}

// openTestRepoWithDB opens a SQLite repository and also opens the raw sql.DB
// for the same file so tests can manipulate timestamps directly.
func openTestRepoWithDB(t *testing.T) *testRepoWithDB {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "test.db")

	repo, err := repository.Open(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Open a second connection to the same file for raw SQL.
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Close()
		_ = repo.Close()
	})
	return &testRepoWithDB{repo: repo, db: db}
}

// backdateByID sets updated_at for the given object ID to now - age.
func (h *testRepoWithDB) backdateByID(t *testing.T, id int64, age time.Duration) {
	t.Helper()
	past := time.Now().UTC().Add(-age).Format(time.RFC3339Nano)
	_, err := h.db.ExecContext(context.Background(),
		`UPDATE objects SET updated_at=? WHERE id=?`, past, id)
	if err != nil {
		t.Fatalf("backdate id=%d: %v", id, err)
	}
}

// putTestBlob writes a small blob to storage for the given key.
func putTestBlob(t *testing.T, store storage.Storage, key string) {
	t.Helper()
	_, err := store.Put(context.Background(), key, strings.NewReader("data"), 4, storage.PutOptions{})
	if err != nil {
		t.Fatalf("put blob %q: %v", key, err)
	}
}

// ---------- LifecycleJob tests ----------

// TestLifecycleSweep_SoftDelete_ExpiredObject verifies that an object whose
// updated_at is past the bucket's expire_after_days window is soft-deleted.
func TestLifecycleSweep_SoftDelete_ExpiredObject(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"

	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	// 1-day expiry, default action (soft_delete).
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 1, "soft_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}

	sk := "default/default/expire-me.txt"
	putTestBlob(t, store, sk)
	obj, err := h.repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: "expire-me.txt",
		Backend: "local", StorageKey: sk, Size: 4, ETag: "e1",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Backdate to 3 days ago so it's past the 1-day cutoff.
	h.backdateByID(t, obj.ID, 72*time.Hour)

	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	lj.sweep(ctx)

	// Object should be gone from active set (soft-deleted).
	if _, err := h.repo.GetObject(ctx, tenant, bucket, "expire-me.txt"); err == nil {
		t.Fatal("expected expired object to be soft-deleted, but it still exists")
	}

	// Blob should NOT be deleted (soft_delete leaves storage intact).
	if _, err := store.Stat(ctx, sk); err != nil {
		t.Fatalf("blob should survive soft_delete, but Stat failed: %v", err)
	}
}

// TestLifecycleSweep_HardDelete_ExpiredObject verifies that an object with
// expire_action="hard_delete" is physically removed from both DB and storage.
func TestLifecycleSweep_HardDelete_ExpiredObject(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"

	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 1, "hard_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}

	sk := "default/default/hard-delete.txt"
	putTestBlob(t, store, sk)
	obj, err := h.repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: "hard-delete.txt",
		Backend: "local", StorageKey: sk, Size: 4, ETag: "e2",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	h.backdateByID(t, obj.ID, 72*time.Hour)

	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	lj.sweep(ctx)

	// Blob should be gone from storage.
	if _, err := store.Stat(ctx, sk); err == nil {
		t.Fatal("expected blob to be hard-deleted from storage, but Stat succeeded")
	}

	// DB row should also be gone (hard delete removes all rows for that key).
	// GetObject checks active (non-deleted) rows.
	if _, err := h.repo.GetObject(ctx, tenant, bucket, "hard-delete.txt"); err == nil {
		t.Fatal("expected hard-deleted object to be gone from DB")
	}
}

// TestLifecycleSweep_FreshObject_NotExpired verifies that an object whose
// updated_at is within the expiry window is left untouched.
func TestLifecycleSweep_FreshObject_NotExpired(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"

	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 7, "soft_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}

	sk := "default/default/fresh.txt"
	putTestBlob(t, store, sk)
	if _, err := h.repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: "fresh.txt",
		Backend: "local", StorageKey: sk, Size: 4, ETag: "e3",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Do NOT backdate: object is brand-new, well within the 7-day window.

	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	lj.sweep(ctx)

	if _, err := h.repo.GetObject(ctx, tenant, bucket, "fresh.txt"); err != nil {
		t.Fatalf("fresh object should survive lifecycle sweep: %v", err)
	}
}

// TestLifecycleSweep_LockedObject_NotHardDeleted verifies that an object under
// an active retention lock is not hard-deleted even if expired.
func TestLifecycleSweep_LockedObject_NotHardDeleted(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"

	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 1, "hard_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}

	sk := "default/default/locked.txt"
	putTestBlob(t, store, sk)
	obj, err := h.repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: "locked.txt",
		Backend: "local", StorageKey: sk, Size: 4, ETag: "e4",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Set an active lock (expires in the future).
	lockUntil := time.Now().Add(24 * time.Hour)
	if err := h.repo.SetLockedUntil(ctx, tenant, bucket, "locked.txt", lockUntil); err != nil {
		t.Fatalf("set lock: %v", err)
	}

	// Backdate to trigger expiry.
	h.backdateByID(t, obj.ID, 72*time.Hour)

	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	lj.sweep(ctx)

	// Blob must survive (lock prevents hard delete).
	if _, err := store.Stat(ctx, sk); err != nil {
		t.Fatalf("locked blob should not be deleted: %v", err)
	}

	// Object row must still be active.
	if _, err := h.repo.GetObject(ctx, tenant, bucket, "locked.txt"); err != nil {
		t.Fatalf("locked object should still exist in DB: %v", err)
	}
}

// TestLifecycleSweep_NoBucketLifecycle_NoExpiry verifies that objects in a
// bucket with expire_after_days=0 (not configured) are never expired.
func TestLifecycleSweep_NoBucketLifecycle_NoExpiry(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"

	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	// No lifecycle configured (expire_after_days stays at default 0).

	sk := "default/default/old.txt"
	putTestBlob(t, store, sk)
	obj, err := h.repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: "old.txt",
		Backend: "local", StorageKey: sk, Size: 4, ETag: "e5",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Backdate by a year - still should not expire because no policy set.
	h.backdateByID(t, obj.ID, 365*24*time.Hour)

	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	lj.sweep(ctx)

	if _, err := h.repo.GetObject(ctx, tenant, bucket, "old.txt"); err != nil {
		t.Fatalf("object without lifecycle policy should survive: %v", err)
	}
}

// TestLifecycleSweep_MixedObjects verifies that in a bucket with expiry
// configured, only genuinely expired objects are actioned while fresh ones
// survive.
func TestLifecycleSweep_MixedObjects(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"

	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 3, "soft_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}

	for i, tc := range []struct {
		key     string
		age     time.Duration
		expired bool
	}{
		{"old1.txt", 10 * 24 * time.Hour, true},
		{"old2.txt", 5 * 24 * time.Hour, true},
		{"new1.txt", 1 * time.Hour, false},
		{"new2.txt", 2 * 24 * time.Hour, false},
	} {
		sk := "default/default/" + tc.key
		putTestBlob(t, store, sk)
		obj, err := h.repo.UpsertObject(ctx, repository.Object{
			TenantID: tenant, Bucket: bucket, Key: tc.key,
			Backend: "local", StorageKey: sk, Size: 4, ETag: itoa(i),
		})
		if err != nil {
			t.Fatalf("upsert %s: %v", tc.key, err)
		}
		if tc.age > 0 {
			h.backdateByID(t, obj.ID, tc.age)
		}
	}

	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	lj.sweep(ctx)

	for _, tc := range []struct {
		key     string
		expired bool
	}{
		{"old1.txt", true},
		{"old2.txt", true},
		{"new1.txt", false},
		{"new2.txt", false},
	} {
		_, err := h.repo.GetObject(ctx, tenant, bucket, tc.key)
		if tc.expired && err == nil {
			t.Errorf("%s: expected expired object to be soft-deleted", tc.key)
		}
		if !tc.expired && err != nil {
			t.Errorf("%s: expected fresh object to survive, got err: %v", tc.key, err)
		}
	}
}

// TestLifecycleJob_New_NilLogger verifies that NewLifecycle substitutes
// slog.Default() when nil logger is passed.
func TestLifecycleJob_New_NilLogger(t *testing.T) {
	repo := openTestRepo(t)
	store := openTestStore(t)

	lj := NewLifecycle(repo, store, time.Minute, nil)
	if lj.logger == nil {
		t.Fatal("expected non-nil logger after NewLifecycle with nil")
	}
}

// TestLifecycleJob_Run_ZeroInterval verifies that Run returns immediately when
// interval is zero or negative.
func TestLifecycleJob_Run_ZeroInterval(t *testing.T) {
	repo := openTestRepo(t)
	store := openTestStore(t)

	lj := NewLifecycle(repo, store, 0, newSilentLogger())

	done := make(chan struct{})
	go func() {
		lj.Run(context.Background())
		close(done)
	}()

	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("Run with zero interval did not return promptly")
	}
}

// TestLifecycleJob_Run_CancelledContext smoke-tests that Run respects context
// cancellation.
func TestLifecycleJob_Run_CancelledContext(t *testing.T) {
	repo := openTestRepo(t)
	store := openTestStore(t)

	lj := NewLifecycle(repo, store, 10*time.Millisecond, newSilentLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		lj.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}

// TestLifecycleSweep_HardDelete_StorageMissing verifies that if the blob is
// already absent from storage (independently deleted), hard_delete still
// proceeds with removing the DB row rather than aborting.
//
// BUG REPORT: The current lifecycle.go implementation calls store.Delete and
// then only calls repo.HardDeleteObject if store.Delete succeeds (err == nil).
// storage.Delete returns nil for a missing key (it is idempotent per contract),
// so this should work correctly. Confirmed OK.
func TestLifecycleSweep_HardDelete_StorageMissing(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"

	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 1, "hard_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}

	// Insert DB row but do NOT write a blob (simulates prior out-of-band deletion).
	obj, err := h.repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: "already-gone.txt",
		Backend: "local", StorageKey: "default/default/already-gone.txt",
		Size: 4, ETag: "e6",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	h.backdateByID(t, obj.ID, 72*time.Hour)

	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	lj.sweep(ctx)

	// DB row should be hard-deleted (storage.Delete on missing key is a no-op,
	// so HardDeleteObject should be called).
	if _, err := h.repo.GetObject(ctx, tenant, bucket, "already-gone.txt"); err == nil {
		t.Fatal("expected DB row to be hard-deleted when blob was already absent")
	}
}

// TestLifecycleSweep_EmptyExpiredList verifies that sweep is a no-op when
// ListExpired returns an empty slice.
func TestLifecycleSweep_EmptyExpiredList(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	// No objects inserted at all.
	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	// Must not panic.
	lj.sweep(ctx)
}

// TestLifecycleSweep_ExpiredActionDefault verifies that when expire_action is
// "soft_delete" (the standard default), soft deletion is applied.
func TestLifecycleSweep_ExpiredActionDefault(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"
	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	// Explicitly set soft_delete.
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 1, "soft_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}

	sk := "default/default/soft.txt"
	putTestBlob(t, store, sk)
	obj, err := h.repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: "soft.txt",
		Backend: "local", StorageKey: sk, Size: 4, ETag: "e7",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	h.backdateByID(t, obj.ID, 48*time.Hour)

	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	lj.sweep(ctx)

	// Row should be soft-deleted.
	if _, err := h.repo.GetObject(ctx, tenant, bucket, "soft.txt"); err == nil {
		t.Fatal("expected soft.txt to be soft-deleted")
	}
	// But blob should remain.
	if _, err := store.Stat(ctx, sk); err != nil {
		t.Fatalf("blob must survive soft_delete: %v", err)
	}
}

// TestLifecycleSweep_MultipleExpiredBatched verifies that a larger batch of
// expired objects is handled in one sweep call.
func TestLifecycleSweep_MultipleExpiredBatched(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"
	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 1, "soft_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}

	const count = 10
	for i := 0; i < count; i++ {
		key := "batch-" + itoa(i) + ".txt"
		sk := "default/default/" + key
		putTestBlob(t, store, sk)
		obj, err := h.repo.UpsertObject(ctx, repository.Object{
			TenantID: tenant, Bucket: bucket, Key: key,
			Backend: "local", StorageKey: sk, Size: 4, ETag: itoa(i),
		})
		if err != nil {
			t.Fatalf("upsert %s: %v", key, err)
		}
		h.backdateByID(t, obj.ID, 48*time.Hour)
	}

	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	lj.sweep(ctx)

	for i := 0; i < count; i++ {
		key := "batch-" + itoa(i) + ".txt"
		if _, err := h.repo.GetObject(ctx, tenant, bucket, key); err == nil {
			t.Errorf("%s should have been soft-deleted", key)
		}
	}
}

// TestLifecycleSweep_ExpiredLocked_SoftDelete verifies a subtle interaction:
// when action=soft_delete, the lock check is NOT applied (lock only guards
// hard_delete). An expired locked object should be soft-deleted.
func TestLifecycleSweep_ExpiredLocked_SoftDelete(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"
	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 1, "soft_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}

	sk := "default/default/locked-soft.txt"
	putTestBlob(t, store, sk)
	obj, err := h.repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: "locked-soft.txt",
		Backend: "local", StorageKey: sk, Size: 4, ETag: "e8",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	lockUntil := time.Now().Add(24 * time.Hour)
	if err := h.repo.SetLockedUntil(ctx, tenant, bucket, "locked-soft.txt", lockUntil); err != nil {
		t.Fatalf("set lock: %v", err)
	}
	h.backdateByID(t, obj.ID, 72*time.Hour)

	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	lj.sweep(ctx)

	// Soft-delete should proceed even when locked.
	if _, err := h.repo.GetObject(ctx, tenant, bucket, "locked-soft.txt"); err == nil {
		t.Fatal("expected locked+expired object to be soft-deleted (lock only blocks hard_delete)")
	}
	// Blob must survive (soft_delete).
	if _, err := store.Stat(ctx, sk); err != nil {
		t.Fatalf("blob must survive soft_delete: %v", err)
	}
}

// TestLifecycleSweep_ExpiredLocked_HardDeleteBlocked verifies that a lock in
// force prevents hard_delete.
func TestLifecycleSweep_ExpiredLocked_HardDeleteBlocked(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"
	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 1, "hard_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}

	sk := "default/default/locked-hard.txt"
	putTestBlob(t, store, sk)
	obj, err := h.repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: "locked-hard.txt",
		Backend: "local", StorageKey: sk, Size: 4, ETag: "e9",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Active lock.
	lockUntil := time.Now().Add(24 * time.Hour)
	if err := h.repo.SetLockedUntil(ctx, tenant, bucket, "locked-hard.txt", lockUntil); err != nil {
		t.Fatalf("set lock: %v", err)
	}
	h.backdateByID(t, obj.ID, 72*time.Hour)

	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	lj.sweep(ctx)

	// Blob and row must both survive.
	if _, err := store.Stat(ctx, sk); err != nil {
		t.Fatalf("locked blob must not be hard-deleted: %v", err)
	}
	if _, err := h.repo.GetObject(ctx, tenant, bucket, "locked-hard.txt"); err != nil {
		t.Fatalf("locked object should still exist in DB: %v", err)
	}
}

// TestLifecycleSweep_ExpiredExpiredLock_HardDeleteProceeds verifies that once
// the retention lock has expired (in the past), hard_delete is allowed.
func TestLifecycleSweep_ExpiredExpiredLock_HardDeleteProceeds(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)

	const tenant, bucket = "default", "default"
	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 1, "hard_delete"); err != nil {
		t.Fatalf("set lifecycle: %v", err)
	}

	sk := "default/default/old-lock.txt"
	putTestBlob(t, store, sk)
	obj, err := h.repo.UpsertObject(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: "old-lock.txt",
		Backend: "local", StorageKey: sk, Size: 4, ETag: "e10",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Set an EXPIRED lock (in the past).
	lockUntil := time.Now().Add(-24 * time.Hour)
	if err := h.repo.SetLockedUntil(ctx, tenant, bucket, "old-lock.txt", lockUntil); err != nil {
		t.Fatalf("set lock: %v", err)
	}
	h.backdateByID(t, obj.ID, 72*time.Hour)

	lj := NewLifecycle(h.repo, store, time.Minute, newSilentLogger())
	lj.sweep(ctx)

	// Blob should be hard-deleted (lock is past).
	if _, err := store.Stat(ctx, sk); err == nil {
		t.Fatal("expected blob to be hard-deleted when lock has expired")
	}
	if _, err := h.repo.GetObject(ctx, tenant, bucket, "old-lock.txt"); err == nil {
		t.Fatal("expected DB row to be gone after hard delete")
	}
}

// ---- AC-2: versioned-bucket lifecycle hard_delete must not nuke non-current versions ----

// TestLifecycleSweep_HardDelete_VersionedBucket_PreservesNonCurrentVersions is
// AC-2 T-2: expire_action=hard_delete on a versioned bucket with noncurrent_days
// set preserves non-current version rows and their blobs; only the expired
// current version is purged.
func TestLifecycleSweep_HardDelete_VersionedBucket_PreservesNonCurrentVersions(t *testing.T) {
	ctx := context.Background()
	h := openTestRepoWithDB(t)
	store := openTestStore(t)
	const tenant, bucket, key = "default", "default", "versioned-expire.txt"

	if err := h.repo.CreateBucket(ctx, tenant, bucket); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetBucketVersioning(ctx, tenant, bucket, true); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetBucketLifecycle(ctx, tenant, bucket, 1, "hard_delete"); err != nil {
		t.Fatal(err)
	}
	// 30-day noncurrent window; noncurrentCount is stored but never enforced.
	if err := h.repo.SetBucketNoncurrentVersionLifecycle(ctx, tenant, bucket, 30, 3); err != nil {
		t.Fatal(err)
	}
	k1 := "default/default/" + key + "@v1"
	k2 := "default/default/" + key + "@v2"
	v1, err := h.repo.InsertObjectVersion(ctx, repository.Object{
		TenantID: tenant, Bucket: bucket, Key: key, VersionID: "v1",
		Backend: "local", StorageKey: k1, Size: 5, ETag: "e1",
	})
	if err != nil {
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
	// v1 is now a tombstone whose deleted_at is "now" — inside the 30-day
	// noncurrent window, so sweepNonCurrentVersions must NOT purge it.
	// Backdate the current version past the 1-day expire window.
	h.backdateByID(t, v2.ID, 72*time.Hour)

	NewLifecycle(h.repo, store, time.Minute, newSilentLogger()).sweep(ctx)

	// Assertion A (rows): only the expired current version's row is purged;
	// the tombstone row survives with its deleted_at untouched.
	versions, err := h.repo.ListObjectVersions(ctx, tenant, bucket, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected exactly 1 remaining version, got %d", len(versions))
	}
	if !versions[0].VersionTombstone {
		t.Fatal("remaining version must be the tombstone (non-current)")
	}
	if versions[0].DeletedAt == nil {
		t.Fatal("tombstone deleted_at must be untouched")
	}
	// Assertion B (blobs): non-current blob preserved, expired current blob gone.
	if _, err := store.Stat(ctx, k1); err != nil {
		t.Fatalf("non-current blob must remain: %v", err)
	}
	if _, err := store.Stat(ctx, k2); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("expired current blob must be purged, Stat err=%v", err)
	}

	// Positive control (window semantics not bypassed): once the tombstone's
	// deleted_at passes the 30-day window, sweepNonCurrentVersions purges it.
	past := time.Now().UTC().Add(-31 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := h.db.ExecContext(ctx, `UPDATE objects SET deleted_at=? WHERE id=?`, past, v1.ID); err != nil {
		t.Fatal(err)
	}
	NewLifecycle(h.repo, store, time.Minute, newSilentLogger()).sweep(ctx)
	versions, err = h.repo.ListObjectVersions(ctx, tenant, bucket, key)
	if err != nil || len(versions) != 0 {
		t.Fatalf("non-current version past its window must be purged: versions=%+v err=%v", versions, err)
	}
}

package service

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func newTestSvc(t *testing.T) (*FileService, repository.Repository) {
	t.Helper()
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if err := repo.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(t.TempDir(), "obj")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	return NewFileService(store, repo, nil), repo
}

func putTestObject(t *testing.T, svc *FileService, key, body string) repository.Object {
	t.Helper()
	ctx := context.Background()
	obj, err := svc.Put(ctx, "", "", key, strings.NewReader(body), int64(len(body)), PutOptions{})
	if err != nil {
		t.Fatalf("put %q: %v", key, err)
	}
	return obj
}

// ---------------------------------------------------------------------------
// file_crud.go — Content-MD5
// ---------------------------------------------------------------------------

func TestPutContentMD5_Valid(t *testing.T) {
	svc, _ := newTestSvc(t)
	body := "hello world"
	// echo -n "hello world" | md5sum  -> 5eb63bbbe01eeed093cb22bb8f5acdc3
	// base64 of that hex -> base64 of the RAW bytes, not the hex string
	// raw MD5 bytes: [0x5e 0xb6 0x3b 0xbb 0xe0 0x1e 0xee 0xd0 0x93 0xcb 0x22 0xbb 0x8f 0x5a 0xcd 0xc3]
	// base64: XrY7u+Ae7tCTyyK7j1rNww==
	const md5b64 = "XrY7u+Ae7tCTyyK7j1rNww=="
	_, err := svc.Put(context.Background(), "", "", "ok.txt", strings.NewReader(body), int64(len(body)), PutOptions{
		ContentMD5: md5b64,
	})
	if err != nil {
		t.Fatalf("put with valid md5: %v", err)
	}
}

func TestPutContentMD5_Mismatch(t *testing.T) {
	svc, _ := newTestSvc(t)
	_, err := svc.Put(context.Background(), "", "", "bad.txt", strings.NewReader("hello world"), 11, PutOptions{
		ContentMD5: "AAAAAAAAAAAAAAAAAAAAAA==", // all-zero base64
	})
	if err == nil {
		t.Fatal("expected ErrBadDigest, got nil")
	}
	if !errors.Is(err, ErrBadDigest) {
		t.Fatalf("expected ErrBadDigest, got %v", err)
	}
}

func TestPutContentMD5_InvalidBase64(t *testing.T) {
	svc, _ := newTestSvc(t)
	_, err := svc.Put(context.Background(), "", "", "badmd5.txt", strings.NewReader("x"), 1, PutOptions{
		ContentMD5: "not-valid-base64!!!",
	})
	if err == nil {
		t.Fatal("expected error for invalid base64, got nil")
	}
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("expected ErrInvalidArgs, got %v", err)
	}
}

func TestPutContentMD5_EmptySkipsCheck(t *testing.T) {
	svc, _ := newTestSvc(t)
	_, err := svc.Put(context.Background(), "", "", "nocheck.txt", strings.NewReader("any data"), 8, PutOptions{})
	if err != nil {
		t.Fatalf("put without md5 should succeed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// acl.go
// ---------------------------------------------------------------------------

func TestValidACL(t *testing.T) {
	for _, acl := range []string{ACLPrivate, ACLPublicRead, ACLPublicReadWrite, ACLAuthenticatedRead} {
		if !validACL(acl) {
			t.Errorf("validACL(%q) should be true", acl)
		}
	}
	if validACL("invalid-acl") {
		t.Error("validACL('invalid-acl') should be false")
	}
}

func TestPublicReadable(t *testing.T) {
	if !PublicReadable(ACLPublicRead) {
		t.Error("PublicReadable(public-read) should be true")
	}
	if !PublicReadable(ACLPublicReadWrite) {
		t.Error("PublicReadable(public-read-write) should be true")
	}
	if PublicReadable(ACLPrivate) {
		t.Error("PublicReadable(private) should be false")
	}
	if PublicReadable(ACLAuthenticatedRead) {
		t.Error("PublicReadable(authenticated-read) should be false")
	}
}

func TestSetAndGetObjectACL(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "acl-test.txt", "hello")

	if err := svc.SetObjectACL(ctx, "", "", "acl-test.txt", ACLPublicRead); err != nil {
		t.Fatalf("SetObjectACL: %v", err)
	}
	got, err := svc.GetObjectACL(ctx, "", "", "acl-test.txt")
	if err != nil {
		t.Fatalf("GetObjectACL: %v", err)
	}
	if got != ACLPublicRead {
		t.Fatalf("want %q, got %q", ACLPublicRead, got)
	}
}

func TestSetAndGetObjectACL_invalid(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "bad-acl.txt", "x")

	err := svc.SetObjectACL(ctx, "", "", "bad-acl.txt", "bogus")
	if err == nil {
		t.Fatal("expected error for invalid ACL")
	}
}

func TestGetObjectACL_notFound(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	_, err := svc.GetObjectACL(ctx, "", "", "nonexistent")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSetAndGetBucketACL(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestSvc(t)
	if err := repo.CreateBucket(ctx, "", "mybucket"); err != nil {
		t.Fatal(err)
	}

	if err := svc.SetBucketACL(ctx, "", "mybucket", ACLPublicRead); err != nil {
		t.Fatalf("SetBucketACL: %v", err)
	}
	cfg, err := svc.GetBucketConfig(ctx, "", "mybucket")
	if err != nil {
		t.Fatalf("GetBucketConfig: %v", err)
	}
	if cfg.ACL != ACLPublicRead {
		t.Fatalf("bucket ACL: want %q, got %q", ACLPublicRead, cfg.ACL)
	}
}

func TestSetBucketACL_invalid(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	err := svc.SetBucketACL(ctx, "", "", "bogus")
	if err == nil {
		t.Fatal("expected error for invalid bucket ACL")
	}
}

func TestObjectPublicReadable(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "pub.txt", "x")

	// Set object ACL to public-read → readable.
	if err := svc.SetObjectACL(ctx, "", "", "pub.txt", ACLPublicRead); err != nil {
		t.Fatal(err)
	}
	if !svc.ObjectPublicReadable(ctx, "", "", "pub.txt") {
		t.Error("expected public-readable after setting object ACL")
	}
}

func TestObjectPublicReadable_fromBucket(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "bpub.txt", "x")

	// Object is private but bucket is public-read.
	if err := svc.SetBucketACL(ctx, "", "", ACLPublicRead); err != nil {
		t.Fatal(err)
	}
	if !svc.ObjectPublicReadable(ctx, "", "", "bpub.txt") {
		t.Error("expected public-readable via bucket ACL")
	}
}

func TestObjectPublicReadable_private(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "priv.txt", "x")

	if svc.ObjectPublicReadable(ctx, "", "", "priv.txt") {
		t.Error("expected not public-readable for private object")
	}
}

func TestObjectPublicReadable_notFound(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	if svc.ObjectPublicReadable(ctx, "", "", "does-not-exist") {
		t.Error("expected false for missing object")
	}
}

// ---------------------------------------------------------------------------
// file.go
// ---------------------------------------------------------------------------

type recordingSink struct {
	events []repository.Event
}

func (s *recordingSink) Publish(ctx context.Context, e repository.Event) {
	s.events = append(s.events, e)
}

func TestNoopSink_Publish(t *testing.T) {
	// noopSink must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("noopSink.Publish panicked: %v", r)
		}
	}()
	var ns noopSink
	ns.Publish(context.Background(), repository.Event{})
}

func TestWithEventSink(t *testing.T) {
	svc, _ := newTestSvc(t)
	sink := &recordingSink{}
	svc.WithEventSink(sink)

	putTestObject(t, svc, "sink-test.txt", "hello")

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sink.events))
	}
	if sink.events[0].Key != "sink-test.txt" {
		t.Fatalf("event key: want %q, got %q", "sink-test.txt", sink.events[0].Key)
	}
}

func TestWithEventSink_nil(t *testing.T) {
	svc, _ := newTestSvc(t)
	svc.WithEventSink(nil)
	// Must not panic on Put.
	putTestObject(t, svc, "nil-sink.txt", "x")
}

func TestWithChunkCleaner(t *testing.T) {
	svc, _ := newTestSvc(t)
	var cleaned int64
	svc.WithChunkCleaner(&mockChunkCleaner{
		fn: func(_ context.Context, objectID int64) error {
			cleaned = objectID
			return nil
		},
	})

	obj := putTestObject(t, svc, "cc-test.txt", "data")
	if err := svc.Delete(context.Background(), "", "", "cc-test.txt", true); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	if cleaned != obj.ID {
		t.Fatalf("chunk cleaner called with objectID=%d, want %d", cleaned, obj.ID)
	}
}

type mockChunkCleaner struct {
	fn func(ctx context.Context, objectID int64) error
}

func (m *mockChunkCleaner) DeleteObjectChunks(ctx context.Context, objectID int64) error {
	return m.fn(ctx, objectID)
}

func TestRepo(t *testing.T) {
	svc, repo := newTestSvc(t)
	if svc.Repo() != repo {
		t.Error("Repo() returned a different repository instance")
	}
}

func TestStorage(t *testing.T) {
	svc, _ := newTestSvc(t)
	if svc.Storage() == nil {
		t.Error("Storage() returned nil")
	}
}

// ---------------------------------------------------------------------------
// file_crud.go
// ---------------------------------------------------------------------------

func TestStat(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	body := "stat-test-body"
	putTestObject(t, svc, "stat.txt", body)

	obj, err := svc.Stat(ctx, "", "", "stat.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if obj.Key != "stat.txt" {
		t.Fatalf("key: want %q, got %q", "stat.txt", obj.Key)
	}
	if obj.Size != int64(len(body)) {
		t.Fatalf("size: want %d, got %d", len(body), obj.Size)
	}
}

func TestStat_notFound(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	_, err := svc.Stat(ctx, "", "", "nonexistent")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete_soft(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "soft-del.txt", "bye")

	if err := svc.Delete(ctx, "", "", "soft-del.txt", false); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	// After soft delete the object should not be found.
	_, err := svc.Stat(ctx, "", "", "soft-del.txt")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after soft delete, got %v", err)
	}
}

func TestDelete_hard(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "hard-del.txt", "gone")

	if err := svc.Delete(ctx, "", "", "hard-del.txt", true); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	// After hard delete the object should not be found.
	_, err := svc.Stat(ctx, "", "", "hard-del.txt")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after hard delete, got %v", err)
	}
}

func TestDelete_notFound(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	err := svc.Delete(ctx, "", "", "nonexistent", false)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete_hardLocked(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	obj := putTestObject(t, svc, "locked.txt", "cant-delete")

	future := time.Now().Add(time.Hour)
	if err := svc.LockObject(ctx, "", "", "locked.txt", future); err != nil {
		t.Fatal(err)
	}
	err := svc.Delete(ctx, "", "", "locked.txt", true)
	if err == nil {
		t.Fatal("expected error for hard delete of locked object")
	}

	// Soft delete of a locked object should still succeed.
	if err := svc.Delete(ctx, "", "", obj.Key, false); err != nil {
		t.Fatalf("soft delete of locked object: %v", err)
	}
}

// ---------------------------------------------------------------------------
// file_features.go
// ---------------------------------------------------------------------------

func TestUsage(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	body := "usage-test"
	putTestObject(t, svc, "usage.txt", body)

	q, err := svc.Usage(ctx, "")
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if q.UsedBytes != int64(len(body)) {
		t.Fatalf("UsedBytes: want %d, got %d", len(body), q.UsedBytes)
	}
	if q.UsedObjects != 1 {
		t.Fatalf("UsedObjects: want 1, got %d", q.UsedObjects)
	}
}

func TestUsage_empty(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	q, err := svc.Usage(ctx, "")
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if q.UsedBytes != 0 || q.UsedObjects != 0 {
		t.Fatalf("expected zero usage, got %+v", q)
	}
}

func TestSetQuota(t *testing.T) {
	ctx := context.Background()
	svc, repo := newTestSvc(t)

	if err := svc.SetQuota(ctx, "", 1000, 5); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}
	q, err := repo.GetTenantQuota(ctx, "default")
	if err != nil {
		t.Fatalf("GetTenantQuota: %v", err)
	}
	if q.MaxBytes != 1000 {
		t.Fatalf("MaxBytes: want %d, got %d", 1000, q.MaxBytes)
	}
	if q.MaxObjects != 5 {
		t.Fatalf("MaxObjects: want %d, got %d", 5, q.MaxObjects)
	}
}

func TestSetQuota_zero(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	if err := svc.SetQuota(ctx, "", 0, 0); err != nil {
		t.Fatalf("SetQuota zero: %v", err)
	}
}

func TestSetTags(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "tags.txt", "meta")

	tags := map[string]string{"color": "red", "env": "test"}
	if err := svc.SetTags(ctx, "", "", "tags.txt", tags); err != nil {
		t.Fatalf("SetTags: %v", err)
	}

	obj, err := svc.Stat(ctx, "", "", "tags.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if obj.Tags["color"] != "red" || obj.Tags["env"] != "test" {
		t.Fatalf("tags: want %v, got %v", tags, obj.Tags)
	}
}

func TestSetTags_empty(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "tags-empty.txt", "x")

	if err := svc.SetTags(ctx, "", "", "tags-empty.txt", map[string]string{}); err != nil {
		t.Fatalf("SetTags empty: %v", err)
	}
}

func TestSetTags_notFound(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	err := svc.SetTags(ctx, "", "", "no-such-key", map[string]string{"k": "v"})
	if err == nil {
		t.Fatal("expected error for SetTags on nonexistent object")
	}
}

func TestLockObject(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	putTestObject(t, svc, "lock.txt", "retain")

	future := time.Now().Add(24 * time.Hour)
	if err := svc.LockObject(ctx, "", "", "lock.txt", future); err != nil {
		t.Fatalf("LockObject: %v", err)
	}

	obj, err := svc.Stat(ctx, "", "", "lock.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if obj.LockedUntil == nil {
		t.Fatal("expected LockedUntil to be set")
	}
	if !obj.LockedUntil.After(time.Now()) {
		t.Fatal("LockedUntil should be in the future")
	}
}

func TestLockObject_notFound(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	err := svc.LockObject(ctx, "", "", "nonexistent", time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("expected error for LockObject on nonexistent object")
	}
}

func TestSetBucketObjectLock(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)

	if err := svc.SetBucketObjectLock(ctx, "", "", 3600); err != nil {
		t.Fatalf("SetBucketObjectLock: %v", err)
	}

	cfg, err := svc.GetBucketConfig(ctx, "", "")
	if err != nil {
		t.Fatalf("GetBucketConfig: %v", err)
	}
	if cfg.ObjectLockSeconds != 3600 {
		t.Fatalf("ObjectLockSeconds: want %d, got %d", 3600, cfg.ObjectLockSeconds)
	}
}

func TestSetBucketObjectLock_zero(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	if err := svc.SetBucketObjectLock(ctx, "", "", 0); err != nil {
		t.Fatalf("SetBucketObjectLock zero: %v", err)
	}
}

func TestPut_withBucketObjectLock(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)

	if err := svc.SetBucketObjectLock(ctx, "", "", 60); err != nil {
		t.Fatal(err)
	}

	obj := putTestObject(t, svc, "auto-locked.txt", "locked-content")
	if obj.LockedUntil == nil {
		t.Fatal("expected LockedUntil to be set by bucket object lock")
	}
}

func TestHardDelete_emitEvent(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	sink := &recordingSink{}
	svc.WithEventSink(sink)

	putTestObject(t, svc, "emit-hard.txt", "event")
	if err := svc.Delete(ctx, "", "", "emit-hard.txt", true); err != nil {
		t.Fatal(err)
	}

	// Should have 2 events: created + deleted.
	if len(sink.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(sink.events))
	}
	if sink.events[1].Type != repository.EventDeleted {
		t.Fatalf("second event type: want %v, got %v", repository.EventDeleted, sink.events[1].Type)
	}
}

func TestSoftDelete_emitEvent(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	sink := &recordingSink{}
	svc.WithEventSink(sink)

	putTestObject(t, svc, "emit-soft.txt", "event")
	if err := svc.Delete(ctx, "", "", "emit-soft.txt", false); err != nil {
		t.Fatal(err)
	}

	if len(sink.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(sink.events))
	}
	if sink.events[1].Type != repository.EventDeleted {
		t.Fatalf("second event type: want %v, got %v", repository.EventDeleted, sink.events[1].Type)
	}
}

func TestGet_emitEvent(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestSvc(t)
	sink := &recordingSink{}
	svc.WithEventSink(sink)

	putTestObject(t, svc, "emit-get.txt", "event")
	rc, _, err := svc.Get(ctx, "", "", "emit-get.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = bytes.NewBuffer(nil).ReadFrom(rc)
	rc.Close()

	// events: created + accessed.
	if len(sink.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(sink.events))
	}
	if sink.events[1].Type != repository.EventAccessed {
		t.Fatalf("second event type: want %v, got %v", repository.EventAccessed, sink.events[1].Type)
	}
}

package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// Preserve the original callback's ID, invocation count and error identity.
func (m *mockChunkCleaner) DeleteScopedObjectChunks(ctx context.Context, ref access.ChunkRef) error {
	if err := access.ValidateChunkRef(ref); err != nil {
		return err
	}
	return m.DeleteObjectChunks(ctx, ref.ObjectID)
}

type chunkScopeCleaner struct {
	refs     []access.ChunkRef
	oldCalls int
	err      error
	before   func(context.Context, access.ChunkRef)
}

func (c *chunkScopeCleaner) DeleteObjectChunks(context.Context, int64) error {
	c.oldCalls++
	return errors.New("ID-only cleanup dispatched")
}

func (c *chunkScopeCleaner) DeleteScopedObjectChunks(ctx context.Context, ref access.ChunkRef) error {
	if c.before != nil {
		c.before(ctx, ref)
	}
	c.refs = append(c.refs, ref)
	return c.err
}

type legacyChunkScopeCleaner struct{ calls int }

func (c *legacyChunkScopeCleaner) DeleteObjectChunks(context.Context, int64) error {
	c.calls++
	return errors.New("legacy cleanup dispatched")
}

type chunkScopeStorage struct {
	storage.Storage
	beforeDelete func(context.Context, string)
	deleteErr    error
}

func (s chunkScopeStorage) Delete(ctx context.Context, key string) error {
	s.beforeDelete(ctx, key)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.Storage.Delete(ctx, key)
}

type bucketChunkScopeStorage struct {
	storage.Storage
	sequence []string
}

func (s *bucketChunkScopeStorage) Delete(ctx context.Context, key string) error {
	s.sequence = append(s.sequence, "blob")
	return s.Storage.Delete(ctx, key)
}

func (s *bucketChunkScopeStorage) AbortMultipart(ctx context.Context, key, uploadID string) error {
	s.sequence = append(s.sequence, "abort")
	return s.Storage.AbortMultipart(ctx, key, uploadID)
}

func seedChunkScopeSharedBlob(t *testing.T, svc *FileService, repo repository.Repository) []repository.Object {
	t.Helper()
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "alpha", "docs", true); err != nil {
		t.Fatal(err)
	}
	const blobKey = "opaque/shared@v-not-an-identity"
	if _, err := svc.store.Put(ctx, blobKey, strings.NewReader("body"), 4, storage.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	var versions []repository.Object
	for _, versionID := range []string{"older", "newer"} {
		obj, err := repo.InsertObjectVersion(ctx, repository.Object{
			TenantID: "alpha", Bucket: "docs", Key: "nested/key", VersionID: versionID,
			StorageKey: blobKey, Backend: "local", Size: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, obj)
	}
	if _, err := repo.AddTenantUsage(ctx, "alpha", 8, 2); err != nil {
		t.Fatal(err)
	}
	return versions
}

func chunkScopeRef(obj repository.Object) access.ChunkRef {
	return access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
}

func newChunkScopeService(t *testing.T) (*FileService, repository.Repository, []repository.Object) {
	t.Helper()
	svc, repo := newTestSvc(t)
	ctx := context.Background()
	if err := svc.SetBucketVersioning(ctx, "alpha", "docs", true); err != nil {
		t.Fatal(err)
	}
	var versions []repository.Object
	for range 2 {
		obj, err := svc.Put(ctx, "alpha", "docs", "nested/source@v-user.txt", strings.NewReader("body"), 4, PutOptions{})
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, obj)
	}
	return svc, repo, versions
}

func attachChunkScopeCleaner(
	t *testing.T, svc *FileService, repo repository.Repository, versions []repository.Object, remoteErr error,
) (*chunkScopeCleaner, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	svc.logger = slog.New(slog.NewTextHandler(&logs, nil))
	cleaner := &chunkScopeCleaner{err: remoteErr}
	cleaner.before = func(ctx context.Context, ref access.ChunkRef) {
		obj, err := repo.GetObjectByID(ctx, ref.ObjectID)
		if err != nil || chunkScopeRef(obj) != ref {
			t.Fatalf("cleanup must use the still-present exact row: ref=%+v obj=%+v err=%v", ref, obj, err)
		}
		if _, err := svc.store.Stat(ctx, obj.StorageKey); err != nil {
			t.Fatalf("cleanup must precede blob deletion: %v", err)
		}
		assertChunkScopeUsage(t, repo, 8, 2)
	}
	svc.WithChunkCleaner(cleaner)
	svc.store = chunkScopeStorage{Storage: svc.store, beforeDelete: func(ctx context.Context, key string) {
		for _, obj := range versions {
			if _, err := repo.GetObjectByID(ctx, obj.ID); err != nil {
				t.Fatalf("blob deletion must precede row deletion: %v", err)
			}
			if obj.StorageKey == key && !chunkScopeContains(cleaner.refs, chunkScopeRef(obj)) {
				t.Fatalf("blob %q deleted before exact scoped cleanup", key)
			}
		}
		assertChunkScopeUsage(t, repo, 8, 2)
	}}
	return cleaner, &logs
}

func chunkScopeContains(refs []access.ChunkRef, ref access.ChunkRef) bool {
	for _, got := range refs {
		if got == ref {
			return true
		}
	}
	return false
}

func assertChunkScopeRefs(t *testing.T, cleaner *chunkScopeCleaner, objects ...repository.Object) {
	t.Helper()
	var want []access.ChunkRef
	for _, obj := range objects {
		want = append(want, chunkScopeRef(obj))
	}
	if cleaner.oldCalls != 0 || !reflect.DeepEqual(cleaner.refs, want) {
		t.Fatalf("refs=%+v want=%+v legacy calls=%d", cleaner.refs, want, cleaner.oldCalls)
	}
}

func assertChunkScopeUsage(t *testing.T, repo repository.Repository, bytes, objects int64) {
	t.Helper()
	quota, err := repo.GetTenantQuota(context.Background(), "alpha")
	if err != nil || quota.UsedBytes != bytes || quota.UsedObjects != objects {
		t.Fatalf("quota=%+v err=%v want=%d bytes/%d objects", quota, err, bytes, objects)
	}
}

func runChunkScopeDelete(svc *FileService, versions []repository.Object, mode string) error {
	ctx := context.Background()
	current, previous := versions[1], versions[0]
	switch mode {
	case "hard":
		return svc.Delete(ctx, current.TenantID, current.Bucket, current.Key, true)
	case "soft":
		return svc.Delete(ctx, current.TenantID, current.Bucket, current.Key, false)
	case "version":
		return svc.DeleteVersion(ctx, previous.TenantID, previous.Bucket, previous.Key, previous.VersionID)
	case "current-version":
		return svc.DeleteVersion(ctx, current.TenantID, current.Bucket, current.Key, current.VersionID)
	case "quarantine":
		return svc.QuarantineObjectByID(ctx, current.ID, "test-threat")
	case "historical-quarantine":
		return svc.QuarantineObjectByID(ctx, previous.ID, "test-threat")
	case "bucket":
		return svc.DeleteBucket(ctx, current.TenantID, current.Bucket)
	default:
		return errors.New("unknown test delete mode")
	}
}

func assertChunkScopeDeleteState(t *testing.T, svc *FileService, repo repository.Repository, versions []repository.Object, mode string) {
	t.Helper()
	ctx := context.Background()
	switch mode {
	case "hard", "bucket":
		for _, obj := range versions {
			assertChunkScopeGone(t, svc, repo, obj)
		}
		assertChunkScopeUsage(t, repo, 0, 0)
	case "soft", "quarantine":
		assertChunkScopeSoftDeleted(t, svc, repo, versions[1])
		assertChunkScopeUsage(t, repo, 4, 1)
	default:
		deleted, survivor := versions[0], versions[1]
		if mode == "current-version" {
			deleted, survivor = survivor, deleted
		}
		assertChunkScopeGone(t, svc, repo, deleted)
		current, err := repo.GetObject(ctx, "alpha", "docs", survivor.Key)
		if err != nil || current.ID != survivor.ID {
			t.Fatalf("historical cleanup changed current: %+v %v", current, err)
		}
		if _, err := svc.store.Stat(ctx, current.StorageKey); err != nil {
			t.Fatalf("historical cleanup removed current blob: %v", err)
		}
		assertChunkScopeUsage(t, repo, 4, 1)
	}
}

func assertChunkScopeGone(t *testing.T, svc *FileService, repo repository.Repository, obj repository.Object) {
	t.Helper()
	if _, err := repo.GetObjectByID(context.Background(), obj.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("deleted row %d survived: %v", obj.ID, err)
	}
	if _, err := svc.store.Stat(context.Background(), obj.StorageKey); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("deleted blob %q survived: %v", obj.StorageKey, err)
	}
}

func assertChunkScopeSoftDeleted(t *testing.T, svc *FileService, repo repository.Repository, expected repository.Object) {
	t.Helper()
	obj, err := repo.GetObjectByID(context.Background(), expected.ID)
	if err != nil || obj.DeletedAt == nil {
		t.Fatalf("current row was not soft-deleted: %+v %v", obj, err)
	}
	if _, err := svc.store.Stat(context.Background(), obj.StorageKey); err != nil {
		t.Fatalf("soft deletion removed blob: %v", err)
	}
	assertDeletedFactVisible(t, repo, obj.ID)
}

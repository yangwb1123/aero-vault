package reconcile

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type cleanupFailStorage struct {
	storage.Storage
}

func (cleanupFailStorage) CleanupParts(context.Context, string, string) error {
	return errors.New("temporary cleanup failure")
}

func TestUploadGCKeepsMetadataWhenStorageCleanupFails(t *testing.T) {
	ctx := context.Background()
	repo, err := repository.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "upload-gc.db"))
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
	init, err := store.InitMultipart(ctx, "default/default/stale.bin", storage.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	upload := repository.Upload{
		ID: init.UploadID, TenantID: "default", Bucket: "default", Key: "stale.bin",
		Backend: "local", BackendUID: init.UploadID, StorageKey: init.Key,
	}
	if err := repo.CreateUpload(ctx, upload); err != nil {
		t.Fatal(err)
	}

	job := NewUploadGC(repo, cleanupFailStorage{Storage: store}, time.Minute, time.Hour, nil)
	if purged := job.purgeUploads(ctx, []repository.Upload{upload}); purged != 0 {
		t.Fatalf("purged=%d after failed storage cleanup", purged)
	}
	if _, err := repo.GetUpload(ctx, upload.ID); err != nil {
		t.Fatalf("upload metadata was lost after cleanup failure: %v", err)
	}

	job.store = store
	if purged := job.purgeUploads(ctx, []repository.Upload{upload}); purged != 1 {
		t.Fatalf("purged=%d after successful cleanup", purged)
	}
	if _, err := repo.GetUpload(ctx, upload.ID); !errors.Is(err, repository.ErrUploadNotFound) {
		t.Fatalf("upload still present after successful cleanup: %v", err)
	}
}

func TestMergeUploadCandidatesDeduplicates(t *testing.T) {
	a := repository.Upload{ID: "a"}
	b := repository.Upload{ID: "b"}
	got := mergeUploadCandidates([]repository.Upload{a, b}, []repository.Upload{a})
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("merged uploads = %+v", got)
	}
}

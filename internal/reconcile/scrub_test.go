package reconcile

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type repositoryChunkCleaner struct {
	repo repository.Repository
}

func (c repositoryChunkCleaner) DeleteObjectChunks(ctx context.Context, objectID int64) error {
	return c.repo.DeleteChunksForObject(ctx, objectID)
}

func TestScrubCorruptionRemovesSearchChunks(t *testing.T) {
	ctx := context.Background()
	repo := openTestRepo(t)
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(t.TempDir(), "objects")})
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	fileService := service.NewFileService(store, repo, nil).WithDeleteFailOpen(true)
	body := "trusted content"
	digest := md5.Sum([]byte(body))
	object, err := fileService.Put(
		ctx, "", "", "scrubbed.txt", strings.NewReader(body), int64(len(body)),
		service.PutOptions{ContentMD5: base64.StdEncoding.EncodeToString(digest[:])},
	)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := repo.InsertChunks(ctx, []repository.Chunk{{
		ObjectID: object.ID, TenantID: object.TenantID, Bucket: object.Bucket,
		ObjectKey: object.Key, Content: body,
	}}); err != nil {
		t.Fatalf("insert chunks: %v", err)
	}
	tampered := "altered content"
	if _, err := store.Put(
		ctx, object.StorageKey, strings.NewReader(tampered), int64(len(tampered)), storage.PutOptions{},
	); err != nil {
		t.Fatalf("tamper storage: %v", err)
	}
	job := New(
		repo, store, 0, false, 0, []string{"default"},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	).WithChunkCleaner(repositoryChunkCleaner{repo: repo})
	if err := job.scrubObject(ctx, object); err == nil {
		t.Fatal("expected corruption result")
	}
	chunks, err := repo.ListChunksForObject(ctx, object.ID)
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("corrupt object retained %d chunks", len(chunks))
	}
	reloaded, err := repo.GetObjectByID(ctx, object.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Metadata["_aero_scrub_status"] != "corrupt" {
		t.Fatalf("metadata=%v", reloaded.Metadata)
	}
}

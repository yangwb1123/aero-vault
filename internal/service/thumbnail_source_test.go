package service

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

type unboundThumbnailStorage struct{ storage.Storage }

type generationMismatchThumbnailStorage struct{ storage.Storage }

func (s *generationMismatchThumbnailStorage) GetGenerationBound(
	ctx context.Context, key string, _ storage.ObjectInfo,
) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	// The stream is deliberately returned with a contradictory proof. The
	// service must close it, reject admission, and use the ordinary uncached
	// fallback rather than trusting a dishonest wrapper.
	info.ETag = "contradictory-proof"
	return rc, info, nil
}

func newThumbnailSourceFixture(t *testing.T, store storage.Storage) (*FileService, repository.Repository, repository.Object) {
	t.Helper()
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "objects.db"))
	if err != nil {
		t.Fatalf("repository.Open: %v", err)
	}
	if err := repo.Migrate(context.Background()); err != nil {
		_ = repo.Close()
		t.Fatalf("repo.Migrate: %v", err)
	}
	svc := NewFileService(store, repo, nil).WithDeleteFailOpen(true)
	obj, err := svc.Put(context.Background(), "tenant", "bucket", "photo.png",
		bytes.NewReader([]byte("thumbnail source")), 16, PutOptions{ContentType: "image/png"})
	if err != nil {
		_ = repo.Close()
		t.Fatalf("Put: %v", err)
	}
	return svc, repo, obj
}

func newLocalThumbnailStorage(t *testing.T) *storage.LocalStorage {
	t.Helper()
	store, err := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(t.TempDir(), "objects")})
	if err != nil {
		t.Fatalf("storage.NewLocal: %v", err)
	}
	return store
}

func TestThumbnailSourceUnsupportedWrapperBypassesCacheAdmission(t *testing.T) {
	base := newLocalThumbnailStorage(t)
	svc, repo, obj := newThumbnailSourceFixture(t, &unboundThumbnailStorage{Storage: base})
	defer repo.Close()
	if svc.ThumbnailSourceCacheBound(obj) {
		t.Fatal("wrapper without GenerationBoundStorage must be conservatively uncacheable")
	}
	source, err := svc.OpenThumbnailSource(context.Background(), obj, "")
	if err != nil {
		t.Fatalf("OpenThumbnailSource: %v", err)
	}
	got, readErr := io.ReadAll(source.Reader)
	closeErr := source.Reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close: read=%v close=%v", readErr, closeErr)
	}
	if string(got) != "thumbnail source" {
		t.Fatalf("opened bytes=%q, want source bytes", got)
	}
	if source.Bound {
		t.Fatal("unsupported wrapper returned a cache-bound source")
	}
	if source.Object.VersionID != obj.VersionID || source.Object.ETag != obj.ETag {
		t.Fatalf("opened identity=%+v, want version=%q etag=%q", source.Object, obj.VersionID, obj.ETag)
	}
}

func TestThumbnailSourceGenerationMismatchReturnsUncachedSource(t *testing.T) {
	base := newLocalThumbnailStorage(t)
	store := &generationMismatchThumbnailStorage{Storage: base}
	svc, repo, obj := newThumbnailSourceFixture(t, store)
	defer repo.Close()
	if !svc.ThumbnailSourceCacheBound(obj) {
		t.Fatal("generation-aware wrapper with a complete local marker should be eligible before open")
	}
	source, err := svc.OpenThumbnailSource(context.Background(), obj, "")
	if err != nil {
		t.Fatalf("OpenThumbnailSource after proof mismatch: %v", err)
	}
	got, readErr := io.ReadAll(source.Reader)
	closeErr := source.Reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close: read=%v close=%v", readErr, closeErr)
	}
	if string(got) != "thumbnail source" {
		t.Fatalf("opened bytes=%q, want fallback source bytes", got)
	}
	if source.Bound {
		t.Fatal("generation proof mismatch must never return a cache-bound source")
	}
}

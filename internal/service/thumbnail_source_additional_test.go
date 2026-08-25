package service

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aero-vault/aero-vault/internal/storage"
)

type generationErrorThumbnailStorage struct {
	storage.Storage
	err error
}

func (s *generationErrorThumbnailStorage) GetGenerationBound(
	ctx context.Context, key string, _ storage.ObjectInfo,
) (io.ReadCloser, storage.ObjectInfo, error) {
	return nil, storage.ObjectInfo{}, s.err
}

type generationMismatchFallbackThumbnailStorage struct {
	storage.Storage
	mutate func(storage.ObjectInfo) storage.ObjectInfo
}

func (s *generationMismatchFallbackThumbnailStorage) GetGenerationBound(
	ctx context.Context, key string, _ storage.ObjectInfo,
) (io.ReadCloser, storage.ObjectInfo, error) {
	return nil, storage.ObjectInfo{}, storage.ErrGenerationMismatch
}

func (s *generationMismatchFallbackThumbnailStorage) Get(
	ctx context.Context, key string,
) (io.ReadCloser, storage.ObjectInfo, error) {
	rc, info, err := s.Storage.Get(ctx, key)
	if err != nil {
		return nil, storage.ObjectInfo{}, err
	}
	if s.mutate != nil {
		info = s.mutate(info)
	}
	return rc, info, nil
}

func readThumbnailSource(t *testing.T, source ThumbnailSource) []byte {
	t.Helper()
	got, readErr := io.ReadAll(source.Reader)
	closeErr := source.Reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close: read=%v close=%v", readErr, closeErr)
	}
	return got
}

func TestThumbnailSourceLegacyRowBypassesBoundCache(t *testing.T) {
	base := newLocalThumbnailStorage(t)
	svc, repo, obj := newThumbnailSourceFixture(t, base)
	defer repo.Close()
	ctx := context.Background()
	if err := repo.ReplaceObjectMetadata(ctx, obj.TenantID, obj.Bucket, obj.Key, map[string]string{}); err != nil {
		t.Fatalf("ReplaceObjectMetadata: %v", err)
	}
	legacy, err := repo.GetObject(ctx, obj.TenantID, obj.Bucket, obj.Key)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if svc.ThumbnailSourceCacheBound(legacy) {
		t.Fatal("legacy row without generation metadata must bypass cache admission")
	}
	source, err := svc.OpenThumbnailSource(ctx, legacy, "")
	if err != nil {
		t.Fatalf("OpenThumbnailSource: %v", err)
	}
	if string(readThumbnailSource(t, source)) != "thumbnail source" {
		t.Fatal("legacy row did not return the current source bytes")
	}
	if source.Bound {
		t.Fatal("legacy row unexpectedly returned a generation-bound source")
	}
	if source.Object.VersionID != legacy.VersionID {
		t.Fatalf("legacy fallback version_id=%q, want %q", source.Object.VersionID, legacy.VersionID)
	}
}

func TestThumbnailSourceGenerationBoundNotFoundMapsToNotFound(t *testing.T) {
	base := newLocalThumbnailStorage(t)
	svc, repo, obj := newThumbnailSourceFixture(t, &generationErrorThumbnailStorage{
		Storage: base,
		err:     storage.ErrNotFound,
	})
	defer repo.Close()
	_, err := svc.OpenThumbnailSource(context.Background(), obj, "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("OpenThumbnailSource error=%v, want ErrNotFound", err)
	}
}

func TestThumbnailSourceGenerationBoundErrorPassthrough(t *testing.T) {
	base := newLocalThumbnailStorage(t)
	boom := errors.New("boom")
	svc, repo, obj := newThumbnailSourceFixture(t, &generationErrorThumbnailStorage{
		Storage: base,
		err:     boom,
	})
	defer repo.Close()
	_, err := svc.OpenThumbnailSource(context.Background(), obj, "")
	if !errors.Is(err, boom) {
		t.Fatalf("OpenThumbnailSource error=%v, want %v", err, boom)
	}
}

func TestThumbnailSourceGenerationMismatchVersionSafety(t *testing.T) {
	cases := []struct {
		name          string
		mutate        func(storage.ObjectInfo) storage.ObjectInfo
		clearVersion  bool
		checkMetadata bool
	}{
		{name: "same-generation-shape-preserves-version"},
		{
			name: "changed-generation-clears-version",
			mutate: func(info storage.ObjectInfo) storage.ObjectInfo {
				meta := map[string]string{}
				for k, v := range info.Metadata {
					meta[k] = v
				}
				meta[storage.GenerationMetadataKey] = meta[storage.GenerationMetadataKey] + "-other"
				info.Metadata = meta
				return info
			},
			clearVersion:  true,
			checkMetadata: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := newLocalThumbnailStorage(t)
			store := &generationMismatchFallbackThumbnailStorage{Storage: base, mutate: tc.mutate}
			svc, repo, obj := newThumbnailSourceFixture(t, store)
			defer repo.Close()
			source, err := svc.OpenThumbnailSource(context.Background(), obj, "")
			if err != nil {
				t.Fatalf("OpenThumbnailSource: %v", err)
			}
			if string(readThumbnailSource(t, source)) != "thumbnail source" {
				t.Fatal("generation mismatch fallback did not return the current source bytes")
			}
			if source.Bound {
				t.Fatal("generation mismatch must never return a bound source")
			}
			wantVersionID := obj.VersionID
			if tc.clearVersion {
				wantVersionID = ""
			}
			if source.Object.VersionID != wantVersionID {
				t.Fatalf("fallback version_id=%q, want %q", source.Object.VersionID, wantVersionID)
			}
			if tc.checkMetadata && source.Object.Metadata[storage.GenerationMetadataKey] == obj.Metadata[storage.GenerationMetadataKey] {
				t.Fatalf("fallback generation=%q, want changed generation", source.Object.Metadata[storage.GenerationMetadataKey])
			}
		})
	}
}

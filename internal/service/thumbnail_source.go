package service

import (
	"context"
	"errors"
	"io"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// ThumbnailSource is the result of opening one authoritative object for
// thumbnail generation. Bound is true only when the storage backend proved
// that the returned descriptor belongs to the returned object's generation;
// the caller separately compares that object with its pre-open observation.
type ThumbnailSource struct {
	Reader io.ReadCloser
	Object repository.Object
	Bound  bool
}

// ThumbnailSourceCacheBound reports whether obj can use generation-bound
// thumbnail caching. It is intentionally conservative for legacy rows without
// a storage-generation marker and for backends without the optional capability.
func (s *FileService) ThumbnailSourceCacheBound(obj repository.Object) bool {
	capable := storage.SupportsGenerationBound(s.store)
	_, complete := thumbnailStorageExpectation(obj)
	return capable && complete
}

// OpenThumbnailSource re-observes the requested current or pinned row, then
// opens it through the optional generation-proof reader. A replacement visible
// between statPinned and this observation is returned with its own identity, so
// it cannot be stored under the earlier cache key.
func (s *FileService) OpenThumbnailSource(ctx context.Context, obj repository.Object, versionID string) (ThumbnailSource, error) {
	openedObj, err := s.thumbnailOpenObject(ctx, obj, versionID)
	if err != nil {
		return ThumbnailSource{}, err
	}
	if err := validateSSECRead(openedObj.Metadata, ReadOptions{}); err != nil {
		return ThumbnailSource{}, err
	}
	expected, eligible := thumbnailStorageExpectation(openedObj)
	bound, capable := s.store.(storage.GenerationBoundStorage)
	capable = capable && storage.SupportsGenerationBound(s.store)
	unbound := !capable || !eligible || !sameThumbnailObject(obj, openedObj)
	proofUnsafe := false
	if capable && eligible {
		rc, proof, err := bound.GetGenerationBound(ctx, openedObj.StorageKey, expected)
		if err == nil && rc != nil && thumbnailStorageProofMatches(proof, expected) {
			s.emit(ctx, openedObj, repository.EventAccessed)
			rc = s.wrapReadVerification(openedObj, rc)
			// The backend proof binds these bytes to openedObj. Whether
			// openedObj is still the object observed by the caller's initial
			// Stat is a separate cache-key comparison performed by the
			// thumbnail package; keep Bound about the storage proof itself so
			// the response can use the freshly opened object's validator.
			return ThumbnailSource{Reader: rc, Object: openedObj, Bound: true}, nil
		}
		if rc != nil {
			_ = rc.Close()
		}
		if err == nil {
			err = storage.ErrGenerationMismatch
		}
		if errors.Is(err, storage.ErrGenerationMismatch) {
			proofUnsafe = true
		}
		if !errors.Is(err, storage.ErrUnsupported) && !errors.Is(err, storage.ErrGenerationMismatch) {
			if errors.Is(err, storage.ErrNotFound) {
				return ThumbnailSource{}, ErrNotFound
			}
			return ThumbnailSource{}, err
		}
		unbound = true
	}

	rc, info, err := s.openStorageWithOptions(ctx, openedObj, ReadOptions{})
	if err != nil {
		return ThumbnailSource{}, err
	}
	s.emit(ctx, openedObj, repository.EventAccessed)
	fallbackObj := thumbnailObjectWithStorageInfo(openedObj, info)
	expectedGeneration := openedObj.Metadata[storage.GenerationMetadataKey]
	proofSameGeneration := info.Metadata[storage.GenerationMetadataKey] == expectedGeneration
	proofSameShape := info.Key == expected.Key && info.Size == expected.Size && info.ETag != ""
	if proofUnsafe && (!proofSameGeneration || !proofSameShape) {
		// A generation mismatch means the repository identity cannot be used
		// as a reusable validator for bytes from a different or unknown
		// storage generation. An ETag-only metadata mismatch with the same
		// trusted generation can still use the storage-reported ETag safely.
		fallbackObj.VersionID = ""
	}
	if unbound {
		return ThumbnailSource{
			Reader: s.wrapReadVerification(openedObj, rc), Object: fallbackObj,
		}, nil
	}
	return ThumbnailSource{
		Reader: s.wrapReadVerification(openedObj, rc), Object: fallbackObj, Bound: true,
	}, nil
}

func (s *FileService) thumbnailOpenObject(ctx context.Context, obj repository.Object, versionID string) (repository.Object, error) {
	if versionID == "" {
		return s.Stat(ctx, obj.TenantID, obj.Bucket, obj.Key)
	}
	return s.StatVersionWithOptions(ctx, obj.TenantID, obj.Bucket, obj.Key, versionID, ReadOptions{})
}

func sameThumbnailObject(a, b repository.Object) bool {
	return a.TenantID == b.TenantID && a.Bucket == b.Bucket && a.Key == b.Key &&
		a.VersionID == b.VersionID && a.ETag == b.ETag &&
		a.StorageKey == b.StorageKey && a.Size == b.Size &&
		a.Metadata[storage.GenerationMetadataKey] == b.Metadata[storage.GenerationMetadataKey]
}

func thumbnailObjectWithStorageInfo(obj repository.Object, info storage.ObjectInfo) repository.Object {
	opened := obj
	if info.Key != "" {
		opened.StorageKey = info.Key
	}
	if info.Size >= 0 {
		opened.Size = info.Size
	}
	if info.ETag != "" {
		opened.ETag = info.ETag
	}
	if info.ContentType != "" {
		opened.ContentType = info.ContentType
	}
	if generation := info.Metadata[storage.GenerationMetadataKey]; generation != "" {
		opened.Metadata = cloneMetadata(opened.Metadata)
		opened.Metadata[storage.GenerationMetadataKey] = generation
	}
	return opened
}

func thumbnailStorageExpectation(obj repository.Object) (storage.ObjectInfo, bool) {
	generation := obj.Metadata[storage.GenerationMetadataKey]
	if obj.StorageKey == "" || obj.ETag == "" || obj.Size < 0 || generation == "" {
		return storage.ObjectInfo{}, false
	}
	return storage.ObjectInfo{
		Key:      obj.StorageKey,
		Size:     obj.Size,
		ETag:     obj.ETag,
		Metadata: map[string]string{storage.GenerationMetadataKey: generation},
	}, true
}

func thumbnailStorageProofMatches(got, expected storage.ObjectInfo) bool {
	generation := expected.Metadata[storage.GenerationMetadataKey]
	return generation != "" && got.Key == expected.Key && got.Size == expected.Size &&
		got.ETag == expected.ETag && got.Metadata[storage.GenerationMetadataKey] == generation
}

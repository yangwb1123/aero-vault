package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

var errScopedChunkCleanerRequired = errors.New("scoped chunk cleaner required")

// cleanObjectChunks uses the loaded row, never a storage key or a newer row.
// A configured incompatible cleaner is a fault; only a true nil is optional.
func (s *FileService) cleanObjectChunks(ctx context.Context, obj repository.Object, expected access.ChunkRef) error {
	if s.chunkCleaner == nil {
		return nil
	}
	cleaner, ok := s.chunkCleaner.(access.ScopedChunkCleaner)
	if !ok || nilScopedChunkCleaner(cleaner) {
		return errScopedChunkCleanerRequired
	}
	ref := access.ChunkRef{TenantID: obj.TenantID, Bucket: obj.Bucket, Key: obj.Key, ObjectID: obj.ID}
	if err := access.ValidateChunkRef(ref); err != nil {
		return err
	}
	if ref != expected {
		return fmt.Errorf("%w: loaded object does not match expected chunk scope", access.ErrInvalidArgument)
	}
	return cleaner.DeleteScopedObjectChunks(ctx, ref)
}

func (s *FileService) cleanHardDeleteChunks(ctx context.Context, versions []repository.Object, tenant, bucket, key string) {
	for _, version := range versions {
		if IsDeleteMarker(version) {
			continue
		}
		ref := access.ChunkRef{TenantID: tenant, Bucket: bucket, Key: key, ObjectID: version.ID}
		if err := s.cleanObjectChunks(ctx, version, ref); err != nil {
			s.logger.Warn("chunk cleanup on hard delete failed", "key", key, "version", version.VersionID, "err", err)
		}
	}
}

func nilScopedChunkCleaner(cleaner access.ScopedChunkCleaner) bool {
	if cleaner == nil {
		return true
	}
	value := reflect.ValueOf(cleaner)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

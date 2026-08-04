package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// hardDeleteObject removes the storage blob first, then the metadata row, and
// decrements tenant quota. Blocks on retention-locked objects. The storage-first
// ordering supports WebDAV MOVE rollback (copy → delete source → rollback dest
// if source delete fails). A metadata-first ordering would break rollback.
func (s *FileService) hardDeleteObject(ctx context.Context, obj repository.Object, tenant, bucket, key string) error {
	versions, err := s.versionsForHardDelete(ctx, tenant, bucket, key, obj)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if IsDeleteMarker(version) {
			continue
		}
		if s.chunkCleaner != nil {
			if err := s.chunkCleaner.DeleteObjectChunks(ctx, version.ID); err != nil {
				s.logger.Warn("chunk cleanup on hard delete failed", "key", key, "version", version.VersionID, "err", err)
			}
		}
	}
	deletedKeys := make(map[string]struct{}, len(versions))
	for _, version := range versions {
		if IsDeleteMarker(version) {
			continue
		}
		if _, deleted := deletedKeys[version.StorageKey]; deleted {
			continue
		}
		if err := s.store.Delete(ctx, version.StorageKey); err != nil {
			return fmt.Errorf("storage delete %q: %w", version.StorageKey, err)
		}
		deletedKeys[version.StorageKey] = struct{}{}
	}
	if err := s.repo.HardDeleteObject(ctx, tenant, bucket, key); err != nil {
		return err
	}
	bytes, objects := countedObjectUsage(versions)
	if _, qErr := s.repo.AddTenantUsage(ctx, tenant, -bytes, -objects); qErr != nil {
		s.logger.Warn("quota decrement on hard delete failed", "err", qErr)
	}
	s.emit(ctx, obj, repository.EventDeleted)
	return nil
}

func (s *FileService) versionsForHardDelete(ctx context.Context, tenant, bucket, key string, current repository.Object) ([]repository.Object, error) {
	versions, err := s.repo.ListObjectVersions(ctx, tenant, bucket, key)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		versions = []repository.Object{current}
	}
	for _, version := range versions {
		if err := s.checkObjectProtection(ctx, version); err != nil {
			return nil, err
		}
	}
	return versions, nil
}

// softDeleteObject marks the object as deleted in metadata without touching
// the underlying storage blob. Also cleans up AI chunks inline so they don't
// remain searchable even if the EventBus subscriber drops the deletion event.
func (s *FileService) softDeleteObject(ctx context.Context, obj repository.Object, tenant, bucket, key string) error {
	// Clean up AI chunks before soft-deleting the metadata row. This provides
	// a safety net when the EventBus subscriber (Indexer) drops the deletion
	// event due to buffer overflow. See docs/requirements/expansion-v144.md
	// direction 1, Phase 2.
	if s.chunkCleaner != nil && !IsDeleteMarker(obj) {
		if err := s.chunkCleaner.DeleteObjectChunks(ctx, obj.ID); err != nil {
			s.logger.Warn("chunk cleanup on soft delete failed", "key", key, "err", err)
		}
	}
	if err := s.repo.SoftDeleteObject(ctx, tenant, bucket, key); err != nil {
		return err
	}
	if _, qErr := s.repo.AddTenantUsage(ctx, tenant, -obj.Size, -1); qErr != nil {
		s.logger.Warn("quota decrement on soft delete failed", "err", qErr)
	}
	s.emit(ctx, obj, repository.EventDeleted)
	return nil
}

// Delete removes an object. When hard is true the storage object is also
// removed. Hard delete fails for objects under retention lock.
func (s *FileService) Delete(ctx context.Context, tenant, bucket, key string, hard bool) error {
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return err
	}
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := s.authorizeObject(ctx, access.ActionDelete, obj); err != nil {
		return err
	}
	if hard {
		return s.hardDeleteObject(ctx, obj, tenant, bucket, key)
	}
	return s.softDeleteObject(ctx, obj, tenant, bucket, key)
}

// DeleteVersion permanently removes one exact version and leaves other
// versions intact. If it was current, the repository promotes the newest
// remaining version.
func (s *FileService) DeleteVersion(ctx context.Context, tenant, bucket, key, versionID string) error {
	obj, err := s.objectVersion(ctx, tenant, bucket, key, versionID)
	if err != nil {
		return err
	}
	if err := s.authorizeObject(ctx, access.ActionDelete, obj); err != nil {
		return err
	}
	if err := s.checkObjectProtection(ctx, obj); err != nil {
		return err
	}
	versions, err := s.repo.ListObjectVersions(
		ctx, obj.TenantID, obj.Bucket, obj.Key,
	)
	if err != nil {
		return err
	}
	wasCurrent := len(versions) > 0 && versions[0].ID == obj.ID
	if s.chunkCleaner != nil && !IsDeleteMarker(obj) {
		if err := s.chunkCleaner.DeleteObjectChunks(ctx, obj.ID); err != nil {
			s.logger.Warn("chunk cleanup on version delete failed", "key", key, "version", versionID, "err", err)
		}
	}
	if !IsDeleteMarker(obj) {
		if err := s.store.Delete(ctx, obj.StorageKey); err != nil {
			return fmt.Errorf("storage delete %q: %w", obj.StorageKey, err)
		}
	}
	if err := s.repo.DeleteObjectVersion(ctx, obj.TenantID, obj.Bucket, obj.Key, obj.VersionID); err != nil {
		return err
	}
	bytes, objects := countedObjectUsage([]repository.Object{obj})
	if _, err := s.repo.AddTenantUsage(ctx, obj.TenantID, -bytes, -objects); err != nil {
		s.logger.Warn("quota decrement on version delete failed", "tenant", obj.TenantID, "err", err)
	}
	s.emit(ctx, obj, repository.EventDeleted)
	if wasCurrent {
		if promoted, err := s.repo.GetObject(ctx, obj.TenantID, obj.Bucket, obj.Key); err == nil {
			s.emit(ctx, promoted, repository.EventCreated)
		}
	}
	return nil
}

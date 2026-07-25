package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// hardDeleteObject removes the storage blob first, then the metadata row, and
// decrements tenant quota. Blocks on retention-locked objects. The storage-first
// ordering supports WebDAV MOVE rollback (copy → delete source → rollback dest
// if source delete fails). A metadata-first ordering would break rollback.
func (s *FileService) hardDeleteObject(ctx context.Context, obj repository.Object, tenant, bucket, key string) error {
	if obj.LockedUntil != nil && obj.LockedUntil.After(time.Now()) {
		return fmt.Errorf("%w: hard delete blocked until %s", ErrLocked, obj.LockedUntil.Format(time.RFC3339))
	}
	if obj.Metadata["_aero_legal_hold"] == "ON" {
		return fmt.Errorf("%w: object is under legal hold", ErrLocked)
	}
	helped, err := s.repo.ObjectHasLegalHold(ctx, obj.ID)
	if err != nil {
		return err
	}
	if helped {
		return fmt.Errorf("%w: object is under legal hold", ErrLocked)
	}
	if s.chunkCleaner != nil {
		if err := s.chunkCleaner.DeleteObjectChunks(ctx, obj.ID); err != nil {
			s.logger.Warn("chunk cleanup on hard delete failed", "key", key, "err", err)
		}
	}
	if err := s.store.Delete(ctx, obj.StorageKey); err != nil {
		return fmt.Errorf("storage delete: %w", err)
	}
	if err := s.repo.HardDeleteObject(ctx, tenant, bucket, key); err != nil {
		return err
	}
	if _, qErr := s.repo.AddTenantUsage(ctx, tenant, -obj.Size, -1); qErr != nil {
		s.logger.Warn("quota decrement on hard delete failed", "err", qErr)
	}
	s.emit(ctx, obj, repository.EventDeleted)
	return nil
}

// softDeleteObject marks the object as deleted in metadata without touching
// the underlying storage blob. Also cleans up AI chunks inline so they don't
// remain searchable even if the EventBus subscriber drops the deletion event.
func (s *FileService) softDeleteObject(ctx context.Context, obj repository.Object, tenant, bucket, key string) error {
	// Clean up AI chunks before soft-deleting the metadata row. This provides
	// a safety net when the EventBus subscriber (Indexer) drops the deletion
	// event due to buffer overflow. See docs/requirements/expansion-v144.md
	// direction 1, Phase 2.
	if s.chunkCleaner != nil {
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
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if hard {
		return s.hardDeleteObject(ctx, obj, tenant, bucket, key)
	}
	return s.softDeleteObject(ctx, obj, tenant, bucket, key)
}

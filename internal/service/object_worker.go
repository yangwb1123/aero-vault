package service

import (
	"context"
	"errors"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// SetObjectTagsByID updates tags on one exact version. It is intended for
// asynchronous workers whose event can become stale after a newer upload.
func (s *FileService) SetObjectTagsByID(
	ctx context.Context, objectID int64, tags map[string]string,
) error {
	obj, err := s.repo.GetObjectByID(ctx, objectID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := s.authorizeObject(ctx, access.ActionWrite, obj); err != nil {
		return err
	}
	if err := s.repo.UpdateObjectTagsByID(ctx, objectID, tags); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// QuarantineObjectByID soft-deletes one exact version while preserving the
// FileService deletion side effects: chunk cleanup, quota accounting and event
// publication. Repeated delivery is idempotent.
func (s *FileService) QuarantineObjectByID(ctx context.Context, objectID int64) error {
	obj, err := s.repo.GetObjectByID(ctx, objectID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := s.authorizeObject(ctx, access.ActionDelete, obj); err != nil {
		return err
	}
	// A non-current version is already represented as a tombstone row, so it
	// cannot be soft-deleted a second time without conflating two states.
	// Remove only that infected version; the current version remains intact.
	if obj.VersionTombstone {
		return s.DeleteVersion(ctx, obj.TenantID, obj.Bucket, obj.Key, obj.VersionID)
	}
	if obj.DeletedAt != nil {
		return nil
	}
	if s.chunkCleaner != nil && !IsDeleteMarker(obj) {
		if err := s.chunkCleaner.DeleteObjectChunks(ctx, obj.ID); err != nil {
			s.logger.Warn("chunk cleanup on quarantine failed", "object_id", obj.ID, "err", err)
		}
	}
	if err := s.repo.SoftDeleteObjectByID(ctx, obj.ID); err != nil {
		return err
	}
	bytes, objects := countedObjectUsage([]repository.Object{obj})
	if _, err := s.repo.AddTenantUsage(ctx, obj.TenantID, -bytes, -objects); err != nil {
		s.logger.Warn("quota decrement on quarantine failed", "tenant", obj.TenantID, "err", err)
	}
	s.emit(ctx, obj, repository.EventDeleted)
	return nil
}

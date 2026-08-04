package service

import (
	"context"
	"errors"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

const deleteMarkerMetadataKey = "_aero_delete_marker"

// CreateDeleteMarker preserves all object versions while making the key
// unreadable as the current object.
func (s *FileService) CreateDeleteMarker(
	ctx context.Context, tenant, bucket, key string,
) (repository.Object, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return repository.Object{}, err
	}
	cfg, err := s.repo.GetBucketConfig(ctx, tenant, bucket)
	if err != nil {
		return repository.Object{}, err
	}
	if !cfg.Versioning {
		return repository.Object{}, ErrInvalidArgs
	}
	current, currentErr := s.repo.GetObject(ctx, tenant, bucket, key)
	if currentErr != nil && !errors.Is(currentErr, repository.ErrNotFound) {
		return repository.Object{}, currentErr
	}
	if currentErr == nil {
		if err := s.authorizeObject(ctx, access.ActionDelete, current); err != nil {
			return repository.Object{}, err
		}
	} else if err := s.authorizePath(ctx, access.ActionDelete, tenant, bucket, key); err != nil {
		return repository.Object{}, err
	}
	metadata := map[string]string{deleteMarkerMetadataKey: "true"}
	if currentErr == nil {
		metadata = metadataWithOwner(metadata, ObjectOwner(current))
	}
	marker, err := s.repo.InsertDeleteMarker(ctx, repository.Object{
		TenantID: tenant,
		Bucket:   bucket,
		Key:      key,
		Metadata: metadata,
	})
	if err != nil {
		return repository.Object{}, err
	}
	if currentErr == nil && s.chunkCleaner != nil {
		if err := s.chunkCleaner.DeleteObjectChunks(ctx, current.ID); err != nil {
			s.logger.Warn("chunk cleanup on delete marker failed", "object_id", current.ID, "err", err)
		}
	}
	s.emit(ctx, marker, repository.EventDeleted)
	return marker, nil
}

// IsDeleteMarker reports whether an object row represents an S3 delete marker.
func IsDeleteMarker(obj repository.Object) bool {
	return obj.Metadata[deleteMarkerMetadataKey] == "true"
}

// ListVersionKeys includes keys whose current state is a delete marker.
func (s *FileService) ListVersionKeys(
	ctx context.Context, tenant, bucket, prefix, marker string, limit int,
) ([]string, string, bool, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.ListObjectVersionKeys(ctx, tenant, bucket, prefix, marker, limit)
}

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

const retentionModeMetadataKey = "_aero_object_lock_mode"

// GetObjectRetention resolves either the current row or one exact version.
func (s *FileService) GetObjectRetention(
	ctx context.Context, tenant, bucket, key, versionID string,
) (repository.Object, error) {
	obj, err := s.objectVersion(ctx, tenant, bucket, key, versionID)
	if err != nil {
		return repository.Object{}, err
	}
	if err := s.authorizeObject(ctx, access.ActionRead, obj); err != nil {
		return repository.Object{}, err
	}
	return obj, nil
}

// SetObjectRetention applies WORM retention to one exact object version.
// Existing retention can be extended but never shortened through this API.
func (s *FileService) SetObjectRetention(
	ctx context.Context,
	tenant, bucket, key, versionID, mode string,
	until time.Time,
) (repository.Object, error) {
	mode = strings.ToUpper(strings.TrimSpace(mode))
	if mode != "GOVERNANCE" && mode != "COMPLIANCE" {
		return repository.Object{}, fmt.Errorf("%w: invalid retention mode", ErrInvalidArgs)
	}
	if !until.After(time.Now()) {
		return repository.Object{}, fmt.Errorf("%w: retention date must be in the future", ErrInvalidArgs)
	}
	obj, err := s.objectVersion(ctx, tenant, bucket, key, versionID)
	if err != nil {
		return repository.Object{}, err
	}
	if err := s.authorizeObject(ctx, access.ActionWrite, obj); err != nil {
		return repository.Object{}, err
	}
	if obj.LockedUntil != nil && obj.LockedUntil.After(until) {
		return repository.Object{}, fmt.Errorf("%w: retention cannot be shortened", ErrLocked)
	}
	metadata := cloneMetadata(obj.Metadata)
	metadata[retentionModeMetadataKey] = mode
	if err := s.repo.SetObjectRetention(ctx, obj.ID, until, metadata); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return repository.Object{}, ErrNotFound
		}
		return repository.Object{}, err
	}
	obj.LockedUntil = &until
	obj.Metadata = metadata
	return obj, nil
}

func (s *FileService) objectVersion(
	ctx context.Context, tenant, bucket, key, versionID string,
) (repository.Object, error) {
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return repository.Object{}, err
	}
	var (
		obj    repository.Object
		getErr error
	)
	if versionID == "" {
		obj, getErr = s.repo.GetObject(ctx, tenant, bucket, key)
	} else {
		obj, getErr = s.repo.GetObjectVersion(ctx, tenant, bucket, key, versionID)
	}
	if errors.Is(getErr, repository.ErrNotFound) {
		return repository.Object{}, ErrNotFound
	}
	return obj, getErr
}

// ObjectRetentionMode reports the persisted S3 retention mode.
func ObjectRetentionMode(obj repository.Object) string {
	if mode := obj.Metadata[retentionModeMetadataKey]; mode != "" {
		return mode
	}
	return "GOVERNANCE"
}

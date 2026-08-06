package service

import (
	"context"
	"errors"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// RestoreObject restores the latest user-deleted object without reviving
// historical version tombstones.
func (s *FileService) RestoreObject(ctx context.Context, tenant, bucket, key string) error {
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return err
	}
	if _, err := s.repo.GetObject(ctx, tenant, bucket, key); err == nil {
		return ErrPreconditionFailed
	} else if !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	obj, err := s.restorableObject(ctx, tenant, bucket, key)
	if err != nil {
		return err
	}
	if err := s.authorizeObject(ctx, access.ActionRestore, obj); err != nil {
		return err
	}
	if _, err := s.store.Stat(ctx, obj.StorageKey); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if err := s.preflightQuota(ctx, tenant, obj.Size, 1); err != nil {
		return err
	}
	if err := s.preflightBucketQuota(ctx, tenant, bucket, obj.Size, 1); err != nil {
		return err
	}
	if err := s.repo.RestoreObject(ctx, tenant, bucket, key); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if _, err := s.addTenantUsage(ctx, tenant, UsageObjectRestore, obj.Size, 1); err != nil {
		return err
	}
	obj.DeletedAt = nil
	s.emit(ctx, obj, repository.EventCreated)
	return nil
}

func (s *FileService) restorableObject(ctx context.Context, tenant, bucket, key string) (repository.Object, error) {
	versions, err := s.repo.ListObjectVersions(ctx, tenant, bucket, key)
	if err != nil {
		return repository.Object{}, err
	}
	for _, obj := range versions {
		if obj.DeletedAt != nil && !obj.VersionTombstone {
			return obj, nil
		}
	}
	return repository.Object{}, ErrNotFound
}

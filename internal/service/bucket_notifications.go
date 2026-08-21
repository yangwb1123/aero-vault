package service

import (
	"context"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// GetBucketNotifications returns the notification rules for a bucket.
func (s *FileService) GetBucketNotifications(
	ctx context.Context, tenant, bucket string,
) ([]repository.NotificationRule, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return nil, err
	}
	return s.repo.GetBucketNotifications(ctx, tenant, bucket)
}

// SetBucketNotifications sets the notification rules for a bucket.
func (s *FileService) SetBucketNotifications(
	ctx context.Context,
	tenant, bucket string,
	rules []repository.NotificationRule,
) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	return s.repo.SetBucketNotifications(ctx, tenant, bucket, rules)
}

// DeleteBucketNotifications removes notification rules from a bucket.
func (s *FileService) DeleteBucketNotifications(
	ctx context.Context, tenant, bucket string,
) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	return s.repo.DeleteBucketNotifications(ctx, tenant, bucket)
}

// SetBucketLifecycleFull stores the complete lifecycle configuration.
func (s *FileService) SetBucketLifecycleFull(
	ctx context.Context,
	tenant, bucket string,
	config repository.LifecycleConfig,
) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	if err := validateLifecycleConfig(config); err != nil {
		return err
	}
	return s.repo.SetBucketLifecycleFull(ctx, tenant, bucket, config)
}

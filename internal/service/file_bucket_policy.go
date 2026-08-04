package service

import (
	"context"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/auth"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// SetBucketPolicy sets an IAM-style JSON policy document on a bucket.
func (s *FileService) SetBucketPolicy(ctx context.Context, tenant, bucket, policy string) error {
	tenant, bucket = defaults(tenant, bucket)
	if policy != "" {
		parsed, err := auth.ParsePolicy(policy)
		if err != nil || parsed == nil {
			return fmt.Errorf("%w: invalid bucket policy: %v", ErrInvalidArgs, err)
		}
	}
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	return s.repo.SetBucketPolicy(ctx, tenant, bucket, policy)
}

// GetBucketPolicy returns the IAM-style JSON policy for a bucket, or "".
func (s *FileService) GetBucketPolicy(ctx context.Context, tenant, bucket string) (string, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return "", err
	}
	cfg, err := s.repo.GetBucketConfig(ctx, tenant, bucket)
	if err != nil {
		return "", err
	}
	return cfg.Policy, nil
}

// GetBucketConfig returns the per-bucket policy.
func (s *FileService) GetBucketConfig(
	ctx context.Context, tenant, bucket string,
) (repository.BucketConfig, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.GetBucketConfig(ctx, tenant, bucket)
}

// GetBucketConfigAuthorized returns bucket settings after a management check.
// Protocol adapters that evaluate a bucket policy must use GetBucketConfig so
// they can authorize the request itself before calling an object operation.
func (s *FileService) GetBucketConfigAuthorized(
	ctx context.Context, tenant, bucket string,
) (repository.BucketConfig, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return repository.BucketConfig{}, err
	}
	return s.repo.GetBucketConfig(ctx, tenant, bucket)
}

package service

import (
	"context"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/storage"
)

func (s *FileService) HeadBucket(ctx context.Context, tenant, bucket string) (bool, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionList, tenant, bucket); err != nil {
		return false, err
	}
	return s.repo.BucketExists(ctx, tenant, bucket)
}

func (s *FileService) ListBuckets(ctx context.Context, tenant string) ([]string, error) {
	tenant, _ = defaults(tenant, "")
	if err := s.requireActiveTenant(ctx, tenant); err != nil {
		return nil, err
	}
	buckets, err := s.repo.ListBuckets(ctx, tenant)
	if err != nil || s.authorizer == nil {
		return buckets, err
	}
	visible := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		if s.authorizeBucket(ctx, access.ActionList, tenant, bucket) == nil {
			visible = append(visible, bucket)
		}
	}
	return visible, nil
}

func (s *FileService) DeleteBucket(ctx context.Context, tenant, bucket string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionDelete, tenant, bucket); err != nil {
		return err
	}
	objects, err := s.bucketObjects(ctx, tenant, bucket)
	if err != nil {
		return err
	}
	for _, obj := range objects {
		if err := s.authorizeObject(ctx, access.ActionDelete, obj); err != nil {
			return err
		}
		if err := s.checkObjectProtection(ctx, obj); err != nil {
			return err
		}
	}
	if err := s.deleteBucketData(ctx, tenant, bucket, objects); err != nil {
		return err
	}
	if err := s.repo.DeleteBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	bytes, count := countedObjectUsage(objects)
	if _, err := s.repo.AddTenantUsage(ctx, tenant, -bytes, -count); err != nil {
		s.logger.Warn("quota decrement on bucket delete failed", "tenant", tenant, "err", err)
	}
	return nil
}

func (s *FileService) CreateBucket(ctx context.Context, tenant, bucket string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionCreate, tenant, bucket); err != nil {
		return err
	}
	return s.repo.CreateBucket(ctx, tenant, bucket)
}

func (s *FileService) BucketStats(ctx context.Context, tenant, bucket string) (repository.BucketStats, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionRead, tenant, bucket); err != nil {
		return repository.BucketStats{}, err
	}
	return s.repo.BucketStats(ctx, tenant, bucket)
}

func (s *FileService) GetBucketCORS(ctx context.Context, tenant, bucket string) ([]repository.CORSRule, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return nil, err
	}
	return s.repo.GetBucketCORS(ctx, tenant, bucket)
}

func (s *FileService) SetBucketCORS(ctx context.Context, tenant, bucket string, rules []repository.CORSRule) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	return s.repo.SetBucketCORS(ctx, tenant, bucket, rules)
}

func (s *FileService) DeleteBucketCORS(ctx context.Context, tenant, bucket string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	return s.repo.DeleteBucketCORS(ctx, tenant, bucket)
}

func (s *FileService) SetBucketEncryption(ctx context.Context, tenant, bucket, algorithm, kmsKeyID string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	if algorithm != "AES256" && algorithm != "aws:kms" {
		return fmt.Errorf("%w: unsupported SSE algorithm", ErrInvalidArgs)
	}
	if algorithm == "AES256" && kmsKeyID != "" {
		return fmt.Errorf("%w: KMS key is valid only with aws:kms", ErrInvalidArgs)
	}
	if !storage.SupportsServerSideEncryption(s.store, algorithm, kmsKeyID) {
		return fmt.Errorf("%w: storage backend cannot satisfy bucket SSE", ErrInvalidArgs)
	}
	return s.repo.SetBucketEncryption(ctx, tenant, bucket, algorithm, kmsKeyID)
}

func (s *FileService) DeleteBucketEncryption(ctx context.Context, tenant, bucket string) error {
	return s.mutateBucket(ctx, tenant, bucket, s.repo.DeleteBucketEncryption)
}

func (s *FileService) SetBucketWebsite(ctx context.Context, tenant, bucket string, cfg repository.WebsiteConfig) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	return s.repo.SetBucketWebsite(ctx, tenant, bucket, cfg)
}

func (s *FileService) DeleteBucketWebsite(ctx context.Context, tenant, bucket string) error {
	return s.mutateBucket(ctx, tenant, bucket, s.repo.DeleteBucketWebsite)
}

func (s *FileService) SetBucketQuota(ctx context.Context, tenant, bucket string, maxBytes, maxObjects int64) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	return s.repo.SetBucketQuota(ctx, tenant, bucket, maxBytes, maxObjects)
}

func (s *FileService) SetBucketTags(ctx context.Context, tenant, bucket string, tags map[string]string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionWrite, tenant, bucket); err != nil {
		return err
	}
	return s.repo.SetBucketTags(ctx, tenant, bucket, tags)
}

func (s *FileService) DeleteBucketTags(ctx context.Context, tenant, bucket string) error {
	return s.mutateBucket(ctx, tenant, bucket, s.repo.DeleteBucketTags)
}

func (s *FileService) SetBucketAccelerate(ctx context.Context, tenant, bucket, status string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	if status != "Enabled" && status != "Suspended" {
		return fmt.Errorf("%w: accelerate status must be Enabled or Suspended", ErrInvalidArgs)
	}
	return s.repo.SetBucketAccelerate(ctx, tenant, bucket, status)
}

func (s *FileService) GetBucketLogging(ctx context.Context, tenant, bucket string) (repository.LoggingConfig, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return repository.LoggingConfig{}, err
	}
	return s.repo.GetBucketLogging(ctx, tenant, bucket)
}

func (s *FileService) SetBucketLogging(ctx context.Context, tenant, bucket, targetBucket, targetPrefix string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	return s.repo.SetBucketLogging(ctx, tenant, bucket, targetBucket, targetPrefix)
}

func (s *FileService) DeleteBucketLogging(ctx context.Context, tenant, bucket string) error {
	return s.mutateBucket(ctx, tenant, bucket, s.repo.DeleteBucketLogging)
}

func (s *FileService) mutateBucket(
	ctx context.Context,
	tenant, bucket string,
	mutation func(context.Context, string, string) error,
) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	return mutation(ctx, tenant, bucket)
}

package service

import (
	"context"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// PresignGet returns a direct-storage download URL. Protocol adapters should
// prefer PreparePresignGet plus an application capability URL so every read
// remains subject to current tenant, policy, ACL, and object state.
func (s *FileService) PresignGet(
	ctx context.Context,
	tenant, bucket, key string,
	expiry time.Duration,
) (string, error) {
	obj, err := s.preparePresignGet(ctx, tenant, bucket, key, expiry)
	if err != nil {
		return "", err
	}
	return s.store.PresignGet(ctx, obj.StorageKey, expiry)
}

// PreparePresignGet applies FileService validation and observability before a
// protocol adapter creates a capability URL that returns through FileService.
func (s *FileService) PreparePresignGet(
	ctx context.Context,
	tenant, bucket, key string,
	expiry time.Duration,
) error {
	_, err := s.preparePresignGet(ctx, tenant, bucket, key, expiry)
	return err
}

func (s *FileService) preparePresignGet(
	ctx context.Context,
	tenant, bucket, key string,
	expiry time.Duration,
) (repository.Object, error) {
	obj, err := s.objectForAction(ctx, tenant, bucket, key, access.ActionDownload)
	if err != nil {
		return repository.Object{}, err
	}
	s.recordPresign(ctx, "GET", obj.TenantID, obj.Bucket, obj.Key, expiry)
	return obj, nil
}

// PresignPut returns a time-limited direct-storage upload URL. Protocol
// adapters should prefer PreparePresignPut plus an application capability URL
// so the upload returns through FileService.
func (s *FileService) PresignPut(
	ctx context.Context,
	tenant, bucket, key string,
	expiry time.Duration,
) (string, error) {
	tenant, bucket, err := s.preparePresignPut(ctx, tenant, bucket, key, expiry)
	if err != nil {
		return "", err
	}
	return s.store.PresignPut(ctx, storageKey(tenant, bucket, key), expiry)
}

// PreparePresignPut applies FileService validation and observability before a
// protocol adapter creates a capability URL that returns through FileService.
func (s *FileService) PreparePresignPut(
	ctx context.Context,
	tenant, bucket, key string,
	expiry time.Duration,
) error {
	_, _, err := s.preparePresignPut(ctx, tenant, bucket, key, expiry)
	return err
}

func (s *FileService) preparePresignPut(
	ctx context.Context,
	tenant, bucket, key string,
	expiry time.Duration,
) (string, string, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return "", "", err
	}
	if _, err := s.preparePutAccess(ctx, tenant, bucket, key, nil); err != nil {
		return "", "", err
	}
	s.recordPresign(ctx, "PUT", tenant, bucket, key, expiry)
	return tenant, bucket, nil
}

func (s *FileService) recordPresign(
	ctx context.Context,
	operation, tenant, bucket, key string,
	expiry time.Duration,
) {
	telemetry.IncPresignGenerated(ctx)
	s.logger.Info("presign generated",
		"operation", operation,
		"tenant", tenant,
		"bucket", bucket,
		"key", key,
		"caller", callerFrom(ctx),
		"expiry", expiry.String(),
	)
}

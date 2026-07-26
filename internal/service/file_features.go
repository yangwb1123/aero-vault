package service

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// Usage returns the tenant's current usage + caps.
func (s *FileService) Usage(ctx context.Context, tenant string) (repository.TenantQuota, error) {
	tenant, _ = defaults(tenant, "")
	return s.repo.GetTenantQuota(ctx, tenant)
}

// SetQuota updates the caps for a tenant.
func (s *FileService) SetQuota(ctx context.Context, tenant string, maxBytes, maxObjects int64) error {
	tenant, _ = defaults(tenant, "")
	return s.repo.SetTenantQuota(ctx, tenant, maxBytes, maxObjects)
}

// SetTags overwrites the tag set on an object.
func (s *FileService) SetTags(ctx context.Context, tenant, bucket, key string, tags map[string]string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.UpdateTags(ctx, tenant, bucket, key, tags)
}

// ListVersions returns every version of a key, newest first.
func (s *FileService) ListVersions(ctx context.Context, tenant, bucket, key string) ([]repository.Object, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.ListObjectVersions(ctx, tenant, bucket, key)
}

// GetVersion fetches the content for a specific version_id.
func (s *FileService) GetVersion(ctx context.Context, tenant, bucket, key, versionID string) (io.ReadCloser, repository.Object, error) {
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObjectVersion(ctx, tenant, bucket, key, versionID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, repository.Object{}, ErrNotFound
		}
		return nil, repository.Object{}, err
	}
	rc, _, err := s.store.Get(ctx, obj.StorageKey)
	if err != nil {
		return nil, repository.Object{}, err
	}
	return rc, obj, nil
}

// LockObject sets a retention deadline on a specific object.
func (s *FileService) LockObject(ctx context.Context, tenant, bucket, key string, until time.Time) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetLockedUntil(ctx, tenant, bucket, key, until)
}

// SetBucketVersioning toggles versioning for the given bucket.
func (s *FileService) SetBucketVersioning(ctx context.Context, tenant, bucket string, enabled bool) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketVersioning(ctx, tenant, bucket, enabled)
}

// SetBucketObjectLock configures default retention for the given bucket.
func (s *FileService) SetBucketObjectLock(ctx context.Context, tenant, bucket string, seconds int) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketObjectLock(ctx, tenant, bucket, seconds)
}

// SetBucketLifecycle configures auto-expiry: ExpireAfterDays from updated_at,
// action is "soft_delete" (default) or "hard_delete".
func (s *FileService) SetBucketLifecycle(ctx context.Context, tenant, bucket string, days int, action string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketLifecycle(ctx, tenant, bucket, days, action)
}

// SetBucketPolicy sets an IAM-style JSON policy document on a bucket.
func (s *FileService) SetBucketPolicy(ctx context.Context, tenant, bucket, policy string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketPolicy(ctx, tenant, bucket, policy)
}

// GetBucketPolicy returns the IAM-style JSON policy for a bucket, or "".
func (s *FileService) GetBucketPolicy(ctx context.Context, tenant, bucket string) (string, error) {
	tenant, bucket = defaults(tenant, bucket)
	cfg, err := s.repo.GetBucketConfig(ctx, tenant, bucket)
	if err != nil {
		return "", err
	}
	return cfg.Policy, nil
}

// GetBucketConfig returns the per-bucket policy.
func (s *FileService) GetBucketConfig(ctx context.Context, tenant, bucket string) (repository.BucketConfig, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.GetBucketConfig(ctx, tenant, bucket)
}

// List paginates objects sharing a prefix.
func (s *FileService) List(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (repository.ListPage, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.ListObjects(ctx, tenant, bucket, prefix, marker, limit)
}

// ListDeleted returns soft-deleted objects for the given tenant/bucket/prefix.
func (s *FileService) ListDeleted(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (repository.ListPage, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.ListDeletedObjects(ctx, tenant, bucket, prefix, marker, limit)
}

// ListByTag paginates objects sharing a prefix and carrying the given tag key
// (optionally also matching a tag value).
func (s *FileService) ListByTag(ctx context.Context, tenant, bucket, prefix, marker string, limit int, tagKey, tagValue string) (repository.ListPage, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.ListObjectsByTag(ctx, tenant, bucket, prefix, marker, limit, tagKey, tagValue)
}

// BatchDeleteResult reports the outcome of a single key during a batch delete.
type BatchDeleteResult struct {
	Key     string `json:"key"`
	Deleted bool   `json:"deleted"`
	Error   string `json:"error,omitempty"`
}

// BatchDelete deletes multiple keys in a single operation. The first error for
// each key is reported individually; other keys still proceed. A non-existent
// key is not an error (idempotent).
func (s *FileService) BatchDelete(ctx context.Context, tenant, bucket string, keys []string) []BatchDeleteResult {
	results := make([]BatchDeleteResult, 0, len(keys))
	for _, key := range keys {
		err := s.Delete(ctx, tenant, bucket, key, false)
		r := BatchDeleteResult{Key: key}
		if err == nil || errors.Is(err, ErrNotFound) {
			r.Deleted = true
		} else {
			r.Deleted = false
			r.Error = err.Error()
		}
		results = append(results, r)
	}
	return results
}

// BatchTagResult reports the outcome for a single key during batch tag.
type BatchTagResult struct {
	Key   string `json:"key"`
	Error string `json:"error,omitempty"`
}

// BatchSetTags sets the same tags on multiple keys. Each key is applied
// independently; a missing key returns an error but other keys continue.
func (s *FileService) BatchSetTags(ctx context.Context, tenant, bucket string, keys []string, tags map[string]string) []BatchTagResult {
	results := make([]BatchTagResult, 0, len(keys))
	for _, key := range keys {
		err := s.SetTags(ctx, tenant, bucket, key, tags)
		r := BatchTagResult{Key: key}
		if err != nil {
			r.Error = err.Error()
		}
		results = append(results, r)
	}
	return results
}

// PutLegalHold places a compliance hold on an object, preventing deletion
// (including lifecycle and retention GC). When versionID is empty, the hold
// applies to all versions. Returns ErrNotFound when the object does not exist.
func (s *FileService) PutLegalHold(ctx context.Context, tenant, bucket, key, versionID, reason, createdBy string) error {
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return s.repo.PutLegalHold(ctx, repository.LegalHold{
		ObjectID:   obj.ID,
		TenantID:   tenant,
		VersionID:  versionID,
		HoldReason: reason,
		CreatedBy:  createdBy,
	})
}

// GetLegalHold retrieves the legal hold for a specific object version.
func (s *FileService) GetLegalHold(ctx context.Context, tenant, bucket, key, versionID string) (repository.LegalHold, error) {
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return repository.LegalHold{}, ErrNotFound
		}
		return repository.LegalHold{}, err
	}
	return s.repo.GetLegalHold(ctx, obj.ID, versionID)
}

// RemoveLegalHold removes a compliance hold from an object version.
func (s *FileService) RemoveLegalHold(ctx context.Context, tenant, bucket, key, versionID string) error {
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return s.repo.RemoveLegalHold(ctx, obj.ID, versionID)
}

// ListLegalHolds returns all legal holds for an object.
func (s *FileService) ListLegalHolds(ctx context.Context, tenant, bucket, key string) ([]repository.LegalHold, error) {
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.repo.ListLegalHolds(ctx, obj.ID)
}

// RestoreObject clears the soft-deleted_at flag on an object, restoring it.
func (s *FileService) RestoreObject(ctx context.Context, tenant, bucket, key string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.RestoreObject(ctx, tenant, bucket, key)
}

// PresignGet returns a time-limited download URL.
func (s *FileService) PresignGet(ctx context.Context, tenant, bucket, key string, expiry time.Duration) (string, error) {
	tenant, bucket = defaults(tenant, bucket)
	obj, err := s.repo.GetObject(ctx, tenant, bucket, key)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	caller := callerFrom(ctx)
	telemetry.IncPresignGenerated(ctx)
	s.logger.Info("presign generated",
		"operation", "GET",
		"tenant", tenant,
		"bucket", bucket,
		"key", key,
		"caller", caller,
		"expiry", expiry.String(),
	)
	return s.store.PresignGet(ctx, obj.StorageKey, expiry)
}

// PresignPut returns a time-limited upload URL.
func (s *FileService) PresignPut(ctx context.Context, tenant, bucket, key string, expiry time.Duration) (string, error) {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return "", err
	}
	caller := callerFrom(ctx)
	telemetry.IncPresignGenerated(ctx)
	s.logger.Info("presign generated",
		"operation", "PUT",
		"tenant", tenant,
		"bucket", bucket,
		"key", key,
		"caller", caller,
		"expiry", expiry.String(),
	)
	return s.store.PresignPut(ctx, storageKey(tenant, bucket, key), expiry)
}

// HeadBucket returns whether the bucket exists.
func (s *FileService) HeadBucket(ctx context.Context, tenant, bucket string) (bool, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.BucketExists(ctx, tenant, bucket)
}

// ListBuckets returns the names of all buckets owned by the tenant.
func (s *FileService) ListBuckets(ctx context.Context, tenant string) ([]string, error) {
	tenant, _ = defaults(tenant, "")
	return s.repo.ListBuckets(ctx, tenant)
}

// DeleteBucket permanently removes a bucket and all its objects.
// Returns ErrNotFound when the bucket does not exist.
func (s *FileService) DeleteBucket(ctx context.Context, tenant, bucket string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.DeleteBucket(ctx, tenant, bucket)
}

// CreateBucket creates the bucket if it does not exist.
func (s *FileService) CreateBucket(ctx context.Context, tenant, bucket string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.CreateBucket(ctx, tenant, bucket)
}

// BucketStats returns object count and total size for a bucket.
func (s *FileService) BucketStats(ctx context.Context, tenant, bucket string) (repository.BucketStats, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.BucketStats(ctx, tenant, bucket)
}

// GetBucketCORS returns the CORS rules for a bucket.
func (s *FileService) GetBucketCORS(ctx context.Context, tenant, bucket string) ([]repository.CORSRule, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.GetBucketCORS(ctx, tenant, bucket)
}

// PutMetadata replaces all metadata on an object. A nil map clears metadata.
func (s *FileService) PutMetadata(ctx context.Context, tenant, bucket, key string, meta map[string]string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateMetadata(meta); err != nil {
		return err
	}
	return s.repo.ReplaceObjectMetadata(ctx, tenant, bucket, key, meta)
}

// PatchMetadata merges the given metadata keys into an object's existing metadata.
// Only the provided keys are updated; existing keys not in meta are preserved.
func (s *FileService) PatchMetadata(ctx context.Context, tenant, bucket, key string, meta map[string]string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateMetadata(meta); err != nil {
		return err
	}
	return s.repo.SetObjectMetaKeys(ctx, tenant, bucket, key, meta)
}

// DeleteMetadata clears all metadata from an object.
func (s *FileService) DeleteMetadata(ctx context.Context, tenant, bucket, key string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return err
	}
	return s.repo.ReplaceObjectMetadata(ctx, tenant, bucket, key, nil)
}

// DeleteMetadataKey removes a single metadata key from an object.
func (s *FileService) DeleteMetadataKey(ctx context.Context, tenant, bucket, key, metaKey string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return err
	}
	return s.repo.DeleteObjectMetaKey(ctx, tenant, bucket, key, metaKey)
}

// SetBucketCORS sets CORS rules for a bucket.
func (s *FileService) SetBucketCORS(ctx context.Context, tenant, bucket string, rules []repository.CORSRule) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketCORS(ctx, tenant, bucket, rules)
}

// DeleteBucketCORS deletes CORS rules for a bucket.
func (s *FileService) DeleteBucketCORS(ctx context.Context, tenant, bucket string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.DeleteBucketCORS(ctx, tenant, bucket)
}

// SetBucketEncryption configures per-bucket SSE. algorithm must be "",
// "AES256", or "aws:kms". Empty clears encryption.
func (s *FileService) SetBucketEncryption(ctx context.Context, tenant, bucket, algorithm, kmsKeyID string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketEncryption(ctx, tenant, bucket, algorithm, kmsKeyID)
}

// DeleteBucketEncryption removes per-bucket SSE config, reverting to global
// storage-level encryption (if any).
func (s *FileService) DeleteBucketEncryption(ctx context.Context, tenant, bucket string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.DeleteBucketEncryption(ctx, tenant, bucket)
}

// SetBucketWebsite configures static website hosting for a bucket.
func (s *FileService) SetBucketWebsite(ctx context.Context, tenant, bucket string, cfg repository.WebsiteConfig) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketWebsite(ctx, tenant, bucket, cfg)
}

// DeleteBucketWebsite removes the website configuration from a bucket.
func (s *FileService) DeleteBucketWebsite(ctx context.Context, tenant, bucket string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.DeleteBucketWebsite(ctx, tenant, bucket)
}

// SetBucketQuota sets per-bucket storage limits (0 = unlimited).
func (s *FileService) SetBucketQuota(ctx context.Context, tenant, bucket string, maxBytes, maxObjects int64) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketQuota(ctx, tenant, bucket, maxBytes, maxObjects)
}

// SetBucketTags sets key-value tags on a bucket.
func (s *FileService) SetBucketTags(ctx context.Context, tenant, bucket string, tags map[string]string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketTags(ctx, tenant, bucket, tags)
}

// DeleteBucketTags removes all tags from a bucket.
func (s *FileService) DeleteBucketTags(ctx context.Context, tenant, bucket string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.DeleteBucketTags(ctx, tenant, bucket)
}

// GetBucketLogging returns the access-logging config for a bucket.
func (s *FileService) GetBucketLogging(ctx context.Context, tenant, bucket string) (repository.LoggingConfig, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.GetBucketLogging(ctx, tenant, bucket)
}

// SetBucketLogging sets the access-logging target for a bucket.
func (s *FileService) SetBucketLogging(ctx context.Context, tenant, bucket, targetBucket, targetPrefix string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketLogging(ctx, tenant, bucket, targetBucket, targetPrefix)
}

// DeleteBucketLogging removes access-logging config for a bucket.
func (s *FileService) DeleteBucketLogging(ctx context.Context, tenant, bucket string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.DeleteBucketLogging(ctx, tenant, bucket)
}

// GetBucketNotifications returns the notification rules for a bucket.
func (s *FileService) GetBucketNotifications(ctx context.Context, tenant, bucket string) ([]repository.NotificationRule, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.GetBucketNotifications(ctx, tenant, bucket)
}

// SetBucketNotifications sets the notification rules for a bucket.
func (s *FileService) SetBucketNotifications(ctx context.Context, tenant, bucket string, rules []repository.NotificationRule) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketNotifications(ctx, tenant, bucket, rules)
}

// SetBucketLifecycleFull stores the complete lifecycle configuration.
func (s *FileService) SetBucketLifecycleFull(ctx context.Context, tenant, bucket string, lc repository.LifecycleConfig) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.SetBucketLifecycleFull(ctx, tenant, bucket, lc)
}

// DeleteBucketNotifications removes notification rules for a bucket.
func (s *FileService) DeleteBucketNotifications(ctx context.Context, tenant, bucket string) error {
	tenant, bucket = defaults(tenant, bucket)
	return s.repo.DeleteBucketNotifications(ctx, tenant, bucket)
}

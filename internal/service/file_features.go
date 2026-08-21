package service

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/aero-vault/aero-vault/internal/access"
	"github.com/aero-vault/aero-vault/internal/repository"
)

// Usage returns the tenant's current usage + caps.
func (s *FileService) Usage(ctx context.Context, tenant string) (repository.TenantQuota, error) {
	tenant, _ = defaults(tenant, "")
	if err := s.requireActiveTenant(ctx, tenant); err != nil {
		return repository.TenantQuota{}, err
	}
	return s.repo.GetTenantQuota(ctx, tenant)
}

// SetQuota updates the caps for a tenant.
func (s *FileService) SetQuota(ctx context.Context, tenant string, maxBytes, maxObjects int64) error {
	tenant, _ = defaults(tenant, "")
	if err := validateQuotaLimits(maxBytes, maxObjects); err != nil {
		return err
	}
	return s.repo.SetTenantQuota(ctx, tenant, maxBytes, maxObjects)
}

// SetTags overwrites the tag set on an object.
func (s *FileService) SetTags(ctx context.Context, tenant, bucket, key string, tags map[string]string) error {
	obj, err := s.objectForAction(ctx, tenant, bucket, key, access.ActionWrite)
	if err != nil {
		return err
	}
	return s.repo.UpdateTags(ctx, obj.TenantID, obj.Bucket, obj.Key, tags)
}

// ListVersions returns every version of a key, newest first.
func (s *FileService) ListVersions(ctx context.Context, tenant, bucket, key string) ([]repository.Object, error) {
	tenant, bucket, err := checkedObjectDefaults(tenant, bucket, key)
	if err != nil {
		return nil, err
	}
	if err := s.requireActiveTenant(ctx, tenant); err != nil {
		return nil, err
	}
	versions, err := s.repo.ListObjectVersions(ctx, tenant, bucket, key)
	if err != nil {
		return nil, err
	}
	return s.filterAuthorizedVersions(ctx, versions)
}

// GetVersion fetches the content for a specific version_id.
func (s *FileService) GetVersion(ctx context.Context, tenant, bucket, key, versionID string) (io.ReadCloser, repository.Object, error) {
	return s.GetVersionWithOptions(ctx, tenant, bucket, key, versionID, ReadOptions{})
}

func (s *FileService) GetVersionWithOptions(ctx context.Context, tenant, bucket, key, versionID string, opts ReadOptions) (io.ReadCloser, repository.Object, error) {
	obj, err := s.StatVersionWithOptions(ctx, tenant, bucket, key, versionID, opts)
	if err != nil {
		return nil, repository.Object{}, err
	}
	rc, err := s.openObjectWithOptions(ctx, obj, opts)
	if err != nil {
		return nil, repository.Object{}, err
	}
	return rc, obj, nil
}

// StatVersionWithOptions validates and returns one exact readable version
// without opening its blob.
func (s *FileService) StatVersionWithOptions(
	ctx context.Context,
	tenant, bucket, key, versionID string,
	opts ReadOptions,
) (repository.Object, error) {
	obj, err := s.objectVersion(ctx, tenant, bucket, key, versionID)
	if err != nil {
		return repository.Object{}, err
	}
	if err := checkCorrupt(obj); err != nil {
		return repository.Object{}, err
	}
	if IsDeleteMarker(obj) {
		return repository.Object{}, ErrNotFound
	}
	if err := s.authorizeObject(ctx, access.ActionRead, obj); err != nil {
		return repository.Object{}, err
	}
	if err := validateSSECRead(obj.Metadata, opts); err != nil {
		return repository.Object{}, err
	}
	return obj, nil
}

// LockObject sets a retention deadline on a specific object.
func (s *FileService) LockObject(ctx context.Context, tenant, bucket, key string, until time.Time) error {
	_, err := s.SetObjectRetention(ctx, tenant, bucket, key, "", "GOVERNANCE", until)
	return err
}

// SetBucketVersioning toggles versioning for the given bucket.
func (s *FileService) SetBucketVersioning(ctx context.Context, tenant, bucket string, enabled bool) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	return s.repo.SetBucketVersioning(ctx, tenant, bucket, enabled)
}

// SetBucketObjectLock configures default retention for the given bucket.
func (s *FileService) SetBucketObjectLock(ctx context.Context, tenant, bucket string, seconds int) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	return s.repo.SetBucketObjectLock(ctx, tenant, bucket, seconds)
}

// SetBucketLifecycle configures auto-expiry: ExpireAfterDays from updated_at,
// action is "soft_delete" (default) or "hard_delete".
func (s *FileService) SetBucketLifecycle(ctx context.Context, tenant, bucket string, days int, action string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := s.authorizeBucket(ctx, access.ActionManageACL, tenant, bucket); err != nil {
		return err
	}
	if err := validateLifecycleDays("days", days); err != nil {
		return err
	}
	return s.repo.SetBucketLifecycle(ctx, tenant, bucket, days, action)
}

// List paginates objects sharing a prefix.
func (s *FileService) List(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (repository.ListPage, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.listAuthorizedObjects(ctx, tenant, bucket, prefix, marker, limit, s.repo.ListObjects)
}

// ListDeleted returns soft-deleted objects for the given tenant/bucket/prefix.
func (s *FileService) ListDeleted(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (repository.ListPage, error) {
	tenant, bucket = defaults(tenant, bucket)
	return s.listAuthorizedObjects(ctx, tenant, bucket, prefix, marker, limit, s.repo.ListDeletedObjects)
}

// ListByTag paginates objects sharing a prefix and carrying the given tag key
// (optionally also matching a tag value).
func (s *FileService) ListByTag(ctx context.Context, tenant, bucket, prefix, marker string, limit int, tagKey, tagValue string) (repository.ListPage, error) {
	tenant, bucket = defaults(tenant, bucket)
	fetch := func(
		ctx context.Context, tenant, bucket, prefix, marker string, limit int,
	) (repository.ListPage, error) {
		return s.repo.ListObjectsByTag(ctx, tenant, bucket, prefix, marker, limit, tagKey, tagValue)
	}
	return s.listAuthorizedObjects(ctx, tenant, bucket, prefix, marker, limit, fetch)
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

// PutLegalHold places a compliance hold on one object version, preventing
// deletion (including lifecycle and retention GC). An empty versionID resolves
// to the current version. Returns ErrNotFound when the object does not exist.
func (s *FileService) PutLegalHold(ctx context.Context, tenant, bucket, key, versionID, reason, createdBy string) error {
	obj, err := s.objectVersion(ctx, tenant, bucket, key, versionID)
	if err != nil {
		return err
	}
	if err := s.authorizeObject(ctx, access.ActionWrite, obj); err != nil {
		return err
	}
	return s.repo.PutLegalHold(ctx, repository.LegalHold{
		ObjectID:   obj.ID,
		TenantID:   obj.TenantID,
		VersionID:  obj.VersionID,
		HoldReason: reason,
		CreatedBy:  createdBy,
	})
}

// GetLegalHold retrieves the legal hold for a specific object version.
func (s *FileService) GetLegalHold(ctx context.Context, tenant, bucket, key, versionID string) (repository.LegalHold, error) {
	obj, err := s.objectVersion(ctx, tenant, bucket, key, versionID)
	if err != nil {
		return repository.LegalHold{}, err
	}
	if err := s.authorizeObject(ctx, access.ActionRead, obj); err != nil {
		return repository.LegalHold{}, err
	}
	return s.repo.GetLegalHold(ctx, obj.ID, obj.VersionID)
}

// RemoveLegalHold removes a compliance hold from an object version.
func (s *FileService) RemoveLegalHold(ctx context.Context, tenant, bucket, key, versionID string) error {
	obj, err := s.objectVersion(ctx, tenant, bucket, key, versionID)
	if err != nil {
		return err
	}
	if err := s.authorizeObject(ctx, access.ActionWrite, obj); err != nil {
		return err
	}
	return s.repo.RemoveLegalHold(ctx, obj.ID, obj.VersionID)
}

// ListLegalHolds returns all legal holds for an object.
func (s *FileService) ListLegalHolds(ctx context.Context, tenant, bucket, key string) ([]repository.LegalHold, error) {
	obj, err := s.objectVersion(ctx, tenant, bucket, key, "")
	if err != nil {
		return nil, err
	}
	if err := s.authorizeObject(ctx, access.ActionRead, obj); err != nil {
		return nil, err
	}
	return s.repo.ListLegalHolds(ctx, obj.ID)
}

// PutMetadata replaces user metadata while preserving service-owned metadata.
func (s *FileService) PutMetadata(ctx context.Context, tenant, bucket, key string, meta map[string]string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateMetadata(meta); err != nil {
		return err
	}
	obj, err := s.objectForAction(ctx, tenant, bucket, key, access.ActionWrite)
	if err != nil {
		return err
	}
	replacement := systemMetadata(obj.Metadata)
	for metaKey, value := range meta {
		replacement[metaKey] = value
	}
	return s.repo.ReplaceObjectMetadata(ctx, tenant, bucket, key, replacement)
}

// GetMetadata returns only caller-owned metadata on an active object.
func (s *FileService) GetMetadata(
	ctx context.Context, tenant, bucket, key string,
) (map[string]string, error) {
	obj, err := s.Stat(ctx, tenant, bucket, key)
	if err != nil {
		return nil, err
	}
	meta := userMetadata(obj.Metadata)
	if meta == nil {
		meta = map[string]string{}
	}
	return meta, nil
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
	if _, err := s.objectForAction(ctx, tenant, bucket, key, access.ActionWrite); err != nil {
		return err
	}
	return s.repo.SetObjectMetaKeys(ctx, tenant, bucket, key, meta)
}

// DeleteMetadata clears user metadata while preserving service-owned metadata.
func (s *FileService) DeleteMetadata(ctx context.Context, tenant, bucket, key string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return err
	}
	obj, err := s.objectForAction(ctx, tenant, bucket, key, access.ActionWrite)
	if err != nil {
		return err
	}
	return s.repo.ReplaceObjectMetadata(ctx, tenant, bucket, key, systemMetadata(obj.Metadata))
}

// DeleteMetadataKey removes a single metadata key from an object.
func (s *FileService) DeleteMetadataKey(ctx context.Context, tenant, bucket, key, metaKey string) error {
	tenant, bucket = defaults(tenant, bucket)
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateMetadata(map[string]string{metaKey: ""}); err != nil {
		return err
	}
	if _, err := s.objectForAction(ctx, tenant, bucket, key, access.ActionWrite); err != nil {
		return err
	}
	return s.repo.DeleteObjectMetaKey(ctx, tenant, bucket, key, metaKey)
}

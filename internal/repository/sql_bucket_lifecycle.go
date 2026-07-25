package repository

import (
	"context"
	"time"
)

// SetBucketLifecycle configures auto-expiry for a bucket.
func (s *sqlStore) SetBucketLifecycle(ctx context.Context, tenant, bucket string, expireAfterDays int, expireAction string) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	if expireAction == "" {
		expireAction = "soft_delete"
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET expire_after_days=$1, expire_action=$2 WHERE tenant_id=$3 AND name=$4`),
		expireAfterDays, expireAction, tenant, bucket)
	return err
}

func (s *sqlStore) SetBucketNoncurrentVersionLifecycle(ctx context.Context, tenant, bucket string, noncurrentDays, noncurrentCount int) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET noncurrent_days=$1, noncurrent_count=$2 WHERE tenant_id=$3 AND name=$4`),
		noncurrentDays, noncurrentCount, tenant, bucket)
	return err
}

// ListExpired finds active objects whose bucket has expire_after_days > 0 and
// whose updated_at is older than that window.
func (s *sqlStore) ListExpired(ctx context.Context, limit int) ([]Object, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT o.id, o.tenant_id, o.bucket, o.key, o.version_id, o.backend, o.storage_key, o.size, o.etag, o.content_type,
       o.metadata, o.tags, o.storage_class, o.created_at, o.updated_at, o.deleted_at, o.locked_until,
       b.expire_after_days, b.expire_action
FROM objects o JOIN buckets b ON o.tenant_id = b.tenant_id AND o.bucket = b.name
WHERE o.deleted_at IS NULL AND b.expire_after_days > 0
LIMIT $1`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	now := time.Now()
	for rows.Next() {
		var (
			obj     Object
			metaRaw []byte
			tagRaw  []byte
			created flexTime
			updated flexTime
			deleted flexNullTime
			locked  flexNullTime
			days    int
			action  string
		)
		if err := rows.Scan(&obj.ID, &obj.TenantID, &obj.Bucket, &obj.Key, &obj.VersionID, &obj.Backend, &obj.StorageKey,
			&obj.Size, &obj.ETag, &obj.ContentType, &metaRaw, &tagRaw, &obj.StorageClass, &created, &updated, &deleted, &locked,
			&days, &action); err != nil {
			return nil, err
		}
		obj.CreatedAt = created.Time
		obj.UpdatedAt = updated.Time
		obj.Metadata, _ = unmarshalKV(metaRaw)
		obj.Tags, _ = unmarshalKV(tagRaw)
		if locked.Valid {
			t := locked.Time
			obj.LockedUntil = &t
		}
		if updated.Time.Add(time.Duration(days) * 24 * time.Hour).Before(now) {
			if action != "" {
				if obj.Metadata == nil {
					obj.Metadata = map[string]string{}
				}
				obj.Metadata["__expire_action"] = action
			}
			out = append(out, obj)
		}
	}
	return out, rows.Err()
}

// ListSoftDeletedBefore finds objects soft-deleted before a timestamp.
// Excludes version-tombstone rows (version_tombstone=1).
func (s *sqlStore) ListSoftDeletedBefore(ctx context.Context, before string, limit int) ([]Object, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until, version_tombstone
FROM objects
WHERE deleted_at IS NOT NULL AND deleted_at < $1 AND version_tombstone = 0
ORDER BY deleted_at
LIMIT $2`), before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Object, 0)
	for rows.Next() {
		obj, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, obj)
	}
	return out, rows.Err()
}

// ListExpiredNonCurrentVersions finds version-tombstone rows whose bucket
// has noncurrent_days > 0 and whose deleted_at is older than that window.
func (s *sqlStore) ListExpiredNonCurrentVersions(ctx context.Context, limit int) ([]Object, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT o.id, o.tenant_id, o.bucket, o.key, o.version_id, o.backend, o.storage_key,
       o.size, o.etag, o.content_type, o.metadata, o.tags, o.storage_class,
       o.created_at, o.updated_at, o.deleted_at, o.locked_until, o.version_tombstone,
       b.noncurrent_days
FROM objects o
JOIN buckets b ON o.tenant_id = b.tenant_id AND o.bucket = b.name
WHERE o.version_tombstone = 1
  AND b.noncurrent_days > 0
  AND NOT EXISTS (SELECT 1 FROM legal_holds lh WHERE lh.object_id = o.id)
LIMIT $1`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	now := time.Now()
	for rows.Next() {
		var (
			obj            Object
			metaRaw        []byte
			tagRaw         []byte
			created        flexTime
			updated        flexTime
			deleted        flexNullTime
			locked         flexNullTime
			tombstone      bool
			noncurrentDays int
		)
		if err := rows.Scan(
			&obj.ID, &obj.TenantID, &obj.Bucket, &obj.Key, &obj.VersionID, &obj.Backend, &obj.StorageKey,
			&obj.Size, &obj.ETag, &obj.ContentType, &metaRaw, &tagRaw, &obj.StorageClass,
			&created, &updated, &deleted, &locked, &tombstone,
			&noncurrentDays,
		); err != nil {
			return nil, err
		}
		obj.VersionTombstone = tombstone
		obj.Metadata, _ = unmarshalKV(metaRaw)
		obj.Tags, _ = unmarshalKV(tagRaw)
		obj.CreatedAt = created.Time
		obj.UpdatedAt = updated.Time
		if locked.Valid {
			t := locked.Time
			obj.LockedUntil = &t
		}
		if deleted.Valid && deleted.Time.Add(time.Duration(noncurrentDays)*24*time.Hour).Before(now) {
			t := deleted.Time
			obj.DeletedAt = &t
			out = append(out, obj)
		}
	}
	return out, rows.Err()
}

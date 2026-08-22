package repository

import (
	"context"
	"encoding/json"
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

// LifecycleConfig holds the complete lifecycle policy for a bucket, including
// expiration and transition rules.
type LifecycleConfig struct {
	ExpireAfterDays                  int              `json:"expire_after_days"`
	ExpireAction                     string           `json:"expire_action"`
	NoncurrentDays                   int              `json:"noncurrent_days"`
	NoncurrentCount                  int              `json:"noncurrent_count"`
	TransitionRules                  []TransitionRule `json:"transition_rules,omitempty"`
	NoncurrentTransitionDays         int              `json:"noncurrent_transition_days"`
	NoncurrentTransitionStorageClass string           `json:"noncurrent_transition_storage_class"`
}

// SetBucketLifecycleFull stores a complete lifecycle configuration for a bucket,
// including expiration, noncurrent version expiration, and transition rules.
func (s *sqlStore) SetBucketLifecycleFull(ctx context.Context, tenant, bucket string, lc LifecycleConfig) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	transJSON := ""
	if len(lc.TransitionRules) > 0 {
		b, err := json.Marshal(lc.TransitionRules)
		if err != nil {
			return err
		}
		transJSON = string(b)
	}
	action := lc.ExpireAction
	if action == "" && lc.ExpireAfterDays > 0 {
		action = "soft_delete"
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`
UPDATE buckets SET expire_after_days=$1, expire_action=$2, noncurrent_days=$3, noncurrent_count=$4,
    transition_rules=$5, noncurrent_transition_days=$6, noncurrent_transition_storage_class=$7
WHERE tenant_id=$8 AND name=$9`),
		lc.ExpireAfterDays, action, lc.NoncurrentDays, lc.NoncurrentCount,
		transJSON, lc.NoncurrentTransitionDays, lc.NoncurrentTransitionStorageClass,
		tenant, bucket)
	return err
}

// ListTransitionable finds non-expired objects whose bucket has transition rules
// and whose age qualifies for a storage-class change. Returns objects whose
// updated_at is older than at least one transition rule's days threshold.
func (s *sqlStore) ListTransitionable(ctx context.Context, limit int) ([]Object, error) {
	limit, batch := lifecycleScanLimits(limit)
	return s.scanLifecycleBatches(ctx, limit, batch, `
SELECT o.id, o.tenant_id, o.bucket, o.key, o.version_id, o.backend, o.storage_key,
       o.size, o.etag, o.content_type, o.metadata, o.tags, o.storage_class,
       o.created_at, o.updated_at, o.deleted_at, o.locked_until,
       b.transition_rules
FROM objects o
JOIN buckets b ON o.tenant_id = b.tenant_id AND o.bucket = b.name
WHERE o.deleted_at IS NULL
  AND b.transition_rules != ''
  AND b.transition_rules IS NOT NULL
  AND o.id > $1
ORDER BY o.id ASC
LIMIT $2`, nil, func(rows lifecycleRows) (Object, bool, error) {
		var (
			obj      Object
			metaRaw  []byte
			tagRaw   []byte
			created  flexTime
			updated  flexTime
			deleted  flexNullTime
			locked   flexNullTime
			transRaw string
		)
		if err := rows.Scan(&obj.ID, &obj.TenantID, &obj.Bucket, &obj.Key, &obj.VersionID, &obj.Backend, &obj.StorageKey,
			&obj.Size, &obj.ETag, &obj.ContentType, &metaRaw, &tagRaw, &obj.StorageClass,
			&created, &updated, &deleted, &locked, &transRaw); err != nil {
			return Object{}, false, err
		}
		obj.CreatedAt = created.Time
		obj.UpdatedAt = updated.Time
		obj.Metadata, _ = unmarshalKV(metaRaw)
		obj.Tags, _ = unmarshalKV(tagRaw)
		if locked.Valid {
			t := locked.Time
			obj.LockedUntil = &t
		}
		// Parse transition rules and find the applicable one.
		if transRaw != "" {
			var rules []TransitionRule
			if err := json.Unmarshal([]byte(transRaw), &rules); err == nil {
				age := time.Since(obj.UpdatedAt)
				for _, rule := range rules {
					if age >= time.Duration(rule.Days)*24*time.Hour && obj.StorageClass != rule.StorageClass {
						if obj.Metadata == nil {
							obj.Metadata = map[string]string{}
						}
						obj.Metadata["__transition_to"] = rule.StorageClass
						return obj, true, nil
					}
				}
			}
		}
		return obj, false, nil
	})
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
	limit, batch := lifecycleScanLimits(limit)
	now := time.Now()
	return s.scanLifecycleBatches(ctx, limit, batch, `
SELECT o.id, o.tenant_id, o.bucket, o.key, o.version_id, o.backend, o.storage_key, o.size, o.etag, o.content_type,
       o.metadata, o.tags, o.storage_class, o.created_at, o.updated_at, o.deleted_at, o.locked_until,
       b.expire_after_days, b.expire_action
FROM objects o JOIN buckets b ON o.tenant_id = b.tenant_id AND o.bucket = b.name
WHERE o.deleted_at IS NULL AND b.expire_after_days > 0 AND o.id > $1
ORDER BY o.id ASC
LIMIT $2`, nil, func(rows lifecycleRows) (Object, bool, error) {
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
			return Object{}, false, err
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
			return obj, true, nil
		}
		return obj, false, nil
	})
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
WHERE deleted_at IS NOT NULL AND deleted_at < $1 AND version_tombstone = $2
ORDER BY deleted_at
LIMIT $3`), before, false, limit)
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
	_, batch := lifecycleScanLimits(limit)
	now := time.Now()
	return s.scanLifecycleBatches(ctx, limit, batch, `
SELECT o.id, o.tenant_id, o.bucket, o.key, o.version_id, o.backend, o.storage_key,
       o.size, o.etag, o.content_type, o.metadata, o.tags, o.storage_class,
       o.created_at, o.updated_at, o.deleted_at, o.locked_until, o.version_tombstone,
       b.noncurrent_days
FROM objects o
JOIN buckets b ON o.tenant_id = b.tenant_id AND o.bucket = b.name
WHERE o.id > $1
  AND o.version_tombstone = $2
  AND b.noncurrent_days > 0
  AND NOT EXISTS (SELECT 1 FROM legal_holds lh WHERE lh.object_id = o.id)
	ORDER BY o.id ASC
LIMIT $3`, []any{true}, func(rows lifecycleRows) (Object, bool, error) {
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
			return Object{}, false, err
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
			return obj, true, nil
		}
		return obj, false, nil
	})
}

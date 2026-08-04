package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *sqlStore) InsertDeleteMarker(ctx context.Context, obj Object) (Object, error) {
	obj.TenantID = defaultTenant(obj.TenantID)
	if obj.VersionID == "" {
		obj.VersionID = NewVersionID()
	}
	metaBytes, err := jsonOrEmpty(obj.Metadata)
	if err != nil {
		return Object{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Object{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, s.rebind(`
UPDATE objects SET deleted_at=$1, version_tombstone=$2
WHERE tenant_id=$3 AND bucket=$4 AND key=$5 AND deleted_at IS NULL`),
		now, true, obj.TenantID, obj.Bucket, obj.Key); err != nil {
		return Object{}, err
	}
	if err := deleteObjectAccessState(ctx, s, tx, obj.TenantID, obj.Bucket, obj.Key); err != nil {
		return Object{}, err
	}
	query := `
INSERT INTO objects (
  tenant_id, bucket, key, version_id, backend, storage_key, size, etag,
  content_type, metadata, tags, storage_class, created_at, updated_at,
  deleted_at, version_tombstone
) VALUES ($1,$2,$3,$4,'','',0,'','',$5,'{}','',$6,$7,$8,0)
RETURNING id, created_at, updated_at`
	if s.dialect == dialectPostgres {
		query = `
INSERT INTO objects (
  tenant_id, bucket, key, version_id, backend, storage_key, size, etag,
  content_type, metadata, tags, storage_class, created_at, updated_at,
  deleted_at, version_tombstone
) VALUES ($1,$2,$3,$4,'','',0,'','',$5::jsonb,'{}'::jsonb,'',now(),now(),now(),false)
RETURNING id, created_at, updated_at`
	}
	args := []any{obj.TenantID, obj.Bucket, obj.Key, obj.VersionID, string(metaBytes)}
	if s.dialect != dialectPostgres {
		args = append(args, now, now, now)
	}
	var created, updated flexTime
	if err := tx.QueryRowContext(ctx, s.rebind(query), args...).Scan(&obj.ID, &created, &updated); err != nil {
		return Object{}, err
	}
	if err := tx.Commit(); err != nil {
		return Object{}, err
	}
	obj.CreatedAt, obj.UpdatedAt = created.Time, updated.Time
	deletedAt := updated.Time
	obj.DeletedAt = &deletedAt
	return obj, nil
}

func (s *sqlStore) GetObjectVersion(ctx context.Context, tenant, bucket, key, versionID string) (Object, error) {
	tenant = defaultTenant(tenant)
	row := s.db.QueryRowContext(ctx, s.rebind(`
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until, version_tombstone
FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND version_id=$4`), tenant, bucket, key, versionID)
	obj, err := scanObject(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Object{}, ErrNotFound
		}
		return Object{}, err
	}
	return obj, nil
}

func (s *sqlStore) ListObjectVersionKeys(
	ctx context.Context, tenant, bucket, prefix, marker string, limit int,
) ([]string, string, bool, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT DISTINCT key FROM objects
WHERE tenant_id=$1 AND bucket=$2 AND key LIKE $3 ESCAPE '!' AND key > $4
ORDER BY key ASC LIMIT $5`),
		tenant, bucket, literalPrefixPattern(prefix), marker, limit+1,
	)
	if err != nil {
		return nil, "", false, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, "", false, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, err
	}
	hasMore := len(keys) > limit
	if hasMore {
		keys = keys[:limit]
	}
	next := ""
	if hasMore {
		next = keys[len(keys)-1]
	}
	return keys, next, hasMore, nil
}

func (s *sqlStore) ListObjectVersions(ctx context.Context, tenant, bucket, key string) ([]Object, error) {
	tenant = defaultTenant(tenant)
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until, version_tombstone
FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3 ORDER BY updated_at DESC, id DESC`), tenant, bucket, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		obj, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, obj)
	}
	return out, rows.Err()
}

func (s *sqlStore) ListObjectVersionsWithOpts(ctx context.Context, tenant, bucket, key string, opts VersionListOpts) (VersionListPage, error) {
	tenant = defaultTenant(tenant)
	limit := opts.Limit
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	query := `
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until, version_tombstone
FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3
ORDER BY updated_at DESC, id DESC LIMIT $4`
	args := []any{tenant, bucket, key, limit + 1}
	if opts.VersionIDMarker != "" {
		var markerTime flexTime
		var markerID int64
		err := s.db.QueryRowContext(ctx, s.rebind(`
SELECT updated_at, id FROM objects
WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND version_id=$4`),
			tenant, bucket, key, opts.VersionIDMarker,
		).Scan(&markerTime, &markerID)
		if errors.Is(err, sql.ErrNoRows) {
			return VersionListPage{}, ErrNotFound
		}
		if err != nil {
			return VersionListPage{}, err
		}
		query = `
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until, version_tombstone
FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3
  AND (updated_at < $4 OR (updated_at = $5 AND id < $6))
ORDER BY updated_at DESC, id DESC LIMIT $7`
		markerValue := markerTime.Time.UTC().Format(time.RFC3339Nano)
		args = []any{tenant, bucket, key, markerValue, markerValue, markerID, limit + 1}
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(query), args...)
	if err != nil {
		return VersionListPage{}, err
	}
	defer rows.Close()
	var out []Object
	for rows.Next() {
		obj, err := scanObject(rows)
		if err != nil {
			return VersionListPage{}, err
		}
		out = append(out, obj)
	}
	if err := rows.Err(); err != nil {
		return VersionListPage{}, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	nextID := ""
	if hasMore && len(out) > 0 {
		nextID = out[len(out)-1].VersionID
	}
	return VersionListPage{Versions: out, NextVersionID: nextID, HasMore: hasMore}, nil
}

package repository

import (
	"context"
	"database/sql"
	"errors"
)

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

func (s *sqlStore) ListObjectVersions(ctx context.Context, tenant, bucket, key string) ([]Object, error) {
	tenant = defaultTenant(tenant)
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until, version_tombstone
FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3 ORDER BY updated_at DESC`), tenant, bucket, key)
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
	if limit <= 0 {
		limit = 1000
	}
	offset := 0
	if opts.VersionIDMarker != "" {
		err := s.db.QueryRowContext(ctx, s.rebind(`
SELECT COUNT(*) FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND updated_at >= (SELECT updated_at FROM objects WHERE version_id=$4 AND tenant_id=$1 AND bucket=$2 AND key=$3)`), tenant, bucket, key, opts.VersionIDMarker).Scan(&offset)
		if err != nil {
			return VersionListPage{}, err
		}
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until, version_tombstone
FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3
ORDER BY updated_at DESC LIMIT $4 OFFSET $5`), tenant, bucket, key, limit+1, offset)
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

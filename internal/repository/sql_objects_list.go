package repository

import (
	"context"
)

func (s *sqlStore) ListObjects(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (ListPage, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	q := `
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until, version_tombstone
FROM objects
WHERE tenant_id=$1 AND bucket=$2 AND deleted_at IS NULL AND key LIKE $3 AND key > $4
ORDER BY key ASC
LIMIT $5`
	rows, err := s.db.QueryContext(ctx, s.rebind(q), tenant, bucket, prefix+"%", marker, limit+1)
	if err != nil {
		return ListPage{}, err
	}
	defer rows.Close()

	var page ListPage
	for rows.Next() {
		obj, err := scanObject(rows)
		if err != nil {
			return ListPage{}, err
		}
		page.Objects = append(page.Objects, obj)
	}
	if len(page.Objects) > limit {
		page.Objects = page.Objects[:limit]
		page.HasMore = true
		page.NextMarker = page.Objects[len(page.Objects)-1].Key
	}
	return page, nil
}

// ListDeletedObjects lists soft-deleted objects. Uses the same pagination as
// ListObjects but filters for deleted_at IS NOT NULL.
func (s *sqlStore) ListDeletedObjects(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (ListPage, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until, version_tombstone
FROM objects
WHERE tenant_id=$1 AND bucket=$2 AND deleted_at IS NOT NULL AND key LIKE $3 AND key > $4
ORDER BY key ASC
LIMIT $5`), tenant, bucket, prefix+"%", marker, limit+1)
	if err != nil {
		return ListPage{}, err
	}
	defer rows.Close()
	var page ListPage
	for rows.Next() {
		obj, err := scanObject(rows)
		if err != nil {
			return ListPage{}, err
		}
		page.Objects = append(page.Objects, obj)
	}
	if len(page.Objects) > limit {
		page.Objects = page.Objects[:limit]
		page.HasMore = true
		page.NextMarker = page.Objects[len(page.Objects)-1].Key
	}
	return page, nil
}

// ListObjectsByTag is like ListObjects but additionally filters results to objects
// that carry the given tag key (and optionally a matching tag value). The tag
// filter is applied client-side after fetching the page from the DB.
func (s *sqlStore) ListObjectsByTag(ctx context.Context, tenant, bucket, prefix, marker string, limit int, tagKey, tagValue string) (ListPage, error) {
	page, err := s.ListObjects(ctx, tenant, bucket, prefix, marker, limit)
	if err != nil {
		return page, err
	}
	var filtered []Object
	for _, obj := range page.Objects {
		if obj.Tags == nil {
			continue
		}
		v, ok := obj.Tags[tagKey]
		if !ok {
			continue
		}
		if tagValue != "" && v != tagValue {
			continue
		}
		filtered = append(filtered, obj)
	}
	page.Objects = filtered
	if len(page.Objects) > limit {
		page.Objects = page.Objects[:limit]
		page.HasMore = true
		page.NextMarker = page.Objects[len(page.Objects)-1].Key
	} else {
		page.HasMore = false
	}
	return page, nil
}

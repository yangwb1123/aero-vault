package repository

import (
	"context"
	"strings"
)

func literalPrefixPattern(prefix string) string {
	escaped := strings.NewReplacer(
		"!", "!!",
		"%", "!%",
		"_", "!_",
	).Replace(prefix)
	return escaped + "%"
}

func (s *sqlStore) ListObjects(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (ListPage, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	q := `
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until, version_tombstone
FROM objects
WHERE tenant_id=$1 AND bucket=$2 AND deleted_at IS NULL
  AND key LIKE $3 ESCAPE '!' AND key > $4
ORDER BY key ASC
LIMIT $5`
	rows, err := s.db.QueryContext(
		ctx, s.rebind(q), tenant, bucket, literalPrefixPattern(prefix), marker, limit+1,
	)
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
WHERE tenant_id=$1 AND bucket=$2 AND deleted_at IS NOT NULL
  AND key LIKE $3 ESCAPE '!' AND key > $4
ORDER BY key ASC
LIMIT $5`), tenant, bucket, literalPrefixPattern(prefix), marker, limit+1)
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

// ListObjectsByTag is like ListObjects but additionally filters results to
// objects that carry the given tag key (and optionally a matching tag value).
// It scans raw pages until it finds one extra match so sparse tag sets still
// report a correct continuation marker.
func (s *sqlStore) ListObjectsByTag(ctx context.Context, tenant, bucket, prefix, marker string, limit int, tagKey, tagValue string) (ListPage, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	cursor := marker
	var matches []Object
	for len(matches) <= limit {
		raw, err := s.ListObjects(ctx, tenant, bucket, prefix, cursor, 1000)
		if err != nil {
			return ListPage{}, err
		}
		for _, obj := range raw.Objects {
			value, ok := obj.Tags[tagKey]
			if ok && (tagValue == "" || value == tagValue) {
				matches = append(matches, obj)
				if len(matches) > limit {
					break
				}
			}
		}
		if len(matches) > limit || !raw.HasMore {
			break
		}
		if raw.NextMarker == "" || raw.NextMarker == cursor {
			break
		}
		cursor = raw.NextMarker
	}
	page := ListPage{Objects: matches}
	if len(matches) > limit {
		page.Objects = matches[:limit]
		page.HasMore = true
		page.NextMarker = page.Objects[len(page.Objects)-1].Key
	}
	return page, nil
}

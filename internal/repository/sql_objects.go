package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/aero-vault/aero-vault/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// repoStoreTracer is a package-level OTel tracer for creating spans in
// repository (SQL) operations.
var repoStoreTracer = telemetry.Tracer("aero-vault/repository")

// ── Write operations ───────────────────────────────────────────────────────

func (s *sqlStore) UpsertObject(ctx context.Context, obj Object) (Object, error) {
	ctx, span := repoStoreTracer.Start(ctx, "Repository.UpsertObject",
		trace.WithAttributes(
			attribute.String("tenant", obj.TenantID),
			attribute.String("bucket", obj.Bucket),
			attribute.String("key", obj.Key),
			attribute.Int64("size", obj.Size),
		),
	)
	defer span.End()

	obj.TenantID = defaultTenant(obj.TenantID)
	if obj.VersionID == "" {
		obj.VersionID = NewVersionID()
	}
	metaBytes, err := jsonOrEmpty(obj.Metadata)
	if err != nil {
		return Object{}, err
	}
	tagBytes, err := jsonOrEmpty(obj.Tags)
	if err != nil {
		return Object{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var (
		q    string
		args []any
	)
	switch s.dialect {
	case dialectPostgres:
		q = `
INSERT INTO objects (tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb,$12, now(), now(), NULL, $13)
ON CONFLICT (tenant_id, bucket, key) WHERE deleted_at IS NULL
DO UPDATE SET backend=EXCLUDED.backend, storage_key=EXCLUDED.storage_key, size=EXCLUDED.size, etag=EXCLUDED.etag,
              content_type=EXCLUDED.content_type, metadata=EXCLUDED.metadata, tags=EXCLUDED.tags,
              storage_class=EXCLUDED.storage_class, version_id=EXCLUDED.version_id,
              locked_until=EXCLUDED.locked_until, updated_at=now()
RETURNING id, version_id, created_at, updated_at`
		args = []any{obj.TenantID, obj.Bucket, obj.Key, obj.VersionID, obj.Backend, obj.StorageKey, obj.Size, obj.ETag, obj.ContentType, string(metaBytes), string(tagBytes), obj.StorageClass, nullableTime(obj.LockedUntil)}
	default:
		q = `
INSERT INTO objects (tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14, NULL, $15)
ON CONFLICT (tenant_id, bucket, key) WHERE deleted_at IS NULL
DO UPDATE SET backend=excluded.backend, storage_key=excluded.storage_key, size=excluded.size, etag=excluded.etag,
              content_type=excluded.content_type, metadata=excluded.metadata, tags=excluded.tags,
              storage_class=excluded.storage_class, version_id=excluded.version_id,
              locked_until=excluded.locked_until, updated_at=excluded.updated_at
RETURNING id, version_id, created_at, updated_at`
		args = []any{obj.TenantID, obj.Bucket, obj.Key, obj.VersionID, obj.Backend, obj.StorageKey, obj.Size, obj.ETag, obj.ContentType, string(metaBytes), string(tagBytes), obj.StorageClass, now, now, nullableTime(obj.LockedUntil)}
	}

	row := s.db.QueryRowContext(ctx, s.rebind(q), args...)
	var (
		id       int64
		vid      string
		createdT flexTime
		updatedT flexTime
	)
	if err := row.Scan(&id, &vid, &createdT, &updatedT); err != nil {
		return Object{}, err
	}
	obj.ID = id
	obj.VersionID = vid
	obj.CreatedAt = createdT.Time
	obj.UpdatedAt = updatedT.Time
	return obj, nil
}

func (s *sqlStore) InsertObjectVersion(ctx context.Context, obj Object) (Object, error) {
	obj.TenantID = defaultTenant(obj.TenantID)
	if obj.VersionID == "" {
		obj.VersionID = NewVersionID()
	}
	metaBytes, err := jsonOrEmpty(obj.Metadata)
	if err != nil {
		return Object{}, err
	}
	tagBytes, err := jsonOrEmpty(obj.Tags)
	if err != nil {
		return Object{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Object{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if s.dialect == dialectPostgres {
		_, err = tx.ExecContext(ctx, `UPDATE objects SET deleted_at=now(), version_tombstone=true WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NULL`,
			obj.TenantID, obj.Bucket, obj.Key)
	} else {
		_, err = tx.ExecContext(ctx, s.rebind(`UPDATE objects SET deleted_at=$1, version_tombstone=1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			now, obj.TenantID, obj.Bucket, obj.Key)
	}
	if err != nil {
		return Object{}, err
	}
	var (
		q    string
		args []any
	)
	if s.dialect == dialectPostgres {
		q = `INSERT INTO objects (tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb,$12, now(), now(), NULL, $13)
RETURNING id, version_id, created_at, updated_at`
		args = []any{obj.TenantID, obj.Bucket, obj.Key, obj.VersionID, obj.Backend, obj.StorageKey, obj.Size, obj.ETag, obj.ContentType, string(metaBytes), string(tagBytes), obj.StorageClass, nullableTime(obj.LockedUntil)}
	} else {
		q = `INSERT INTO objects (tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14, NULL, $15)
RETURNING id, version_id, created_at, updated_at`
		args = []any{obj.TenantID, obj.Bucket, obj.Key, obj.VersionID, obj.Backend, obj.StorageKey, obj.Size, obj.ETag, obj.ContentType, string(metaBytes), string(tagBytes), obj.StorageClass, now, now, nullableTime(obj.LockedUntil)}
	}
	row := tx.QueryRowContext(ctx, s.rebind(q), args...)
	var (
		id       int64
		vid      string
		createdT flexTime
		updatedT flexTime
	)
	if err := row.Scan(&id, &vid, &createdT, &updatedT); err != nil {
		return Object{}, err
	}
	if err := tx.Commit(); err != nil {
		return Object{}, err
	}
	obj.ID = id
	obj.VersionID = vid
	obj.CreatedAt = createdT.Time
	obj.UpdatedAt = updatedT.Time
	return obj, nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// ── Read operations ─────────────────────────────────────────────────────────

func (s *sqlStore) GetObject(ctx context.Context, tenant, bucket, key string) (Object, error) {
	ctx, span := repoStoreTracer.Start(ctx, "Repository.GetObject",
		trace.WithAttributes(
			attribute.String("tenant", tenant),
			attribute.String("bucket", bucket),
			attribute.String("key", key),
		),
	)
	defer span.End()

	tenant = defaultTenant(tenant)
	row := s.db.QueryRowContext(ctx, s.rebind(`
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until, version_tombstone
FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NULL`), tenant, bucket, key)
	obj, err := scanObject(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Object{}, ErrNotFound
		}
		return Object{}, err
	}
	return obj, nil
}

func (s *sqlStore) GetObjectByID(ctx context.Context, id int64) (Object, error) {
	row := s.db.QueryRowContext(ctx, s.rebind(`
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, storage_class, created_at, updated_at, deleted_at, locked_until, version_tombstone
FROM objects WHERE id=$1`), id)
	obj, err := scanObject(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Object{}, ErrNotFound
		}
		return Object{}, err
	}
	return obj, nil
}

// StorageClassCounts returns active (non-deleted) object counts grouped by storage_class.
func (s *sqlStore) StorageClassCounts(ctx context.Context, tenant string) (map[string]int64, error) {
	tenant = defaultTenant(tenant)
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT COALESCE(storage_class,'STANDARD'), COUNT(1) FROM objects WHERE tenant_id=$1 AND deleted_at IS NULL GROUP BY storage_class`), tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var cls string
		var count int64
		if err := rows.Scan(&cls, &count); err != nil {
			return nil, err
		}
		if cls == "" {
			cls = "STANDARD"
		}
		out[cls] = count
	}
	return out, nil
}

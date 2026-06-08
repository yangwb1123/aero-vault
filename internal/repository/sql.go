package repository

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cryptoRandRead and binaryPutUint64 are aliases so the helper at the bottom
// of the file stays self-contained.
var (
	cryptoRandRead  = crand.Read
	binaryPutUint64 = binary.LittleEndian.PutUint64
)

//go:embed migrations/postgres/*.sql migrations/sqlite/*.sql
var migrationsFS embed.FS

type dialect int

const (
	dialectSQLite dialect = iota
	dialectPostgres
)

type sqlStore struct {
	db      *sql.DB
	dialect dialect
}

func (s *sqlStore) Close() error                   { return s.db.Close() }
func (s *sqlStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// rebind converts $N placeholders into ? for SQLite.
func (s *sqlStore) rebind(q string) string {
	if s.dialect == dialectPostgres {
		return q
	}
	var b strings.Builder
	b.Grow(len(q))
	for i := 0; i < len(q); i++ {
		if q[i] != '$' {
			b.WriteByte(q[i])
			continue
		}
		j := i + 1
		for j < len(q) && q[j] >= '0' && q[j] <= '9' {
			j++
		}
		if j == i+1 {
			b.WriteByte(q[i])
			continue
		}
		b.WriteByte('?')
		i = j - 1
	}
	return b.String()
}

func (s *sqlStore) Migrate(ctx context.Context) error {
	dir := "migrations/postgres"
	if s.dialect == dialectSQLite {
		dir = "migrations/sqlite"
	}
	files, err := listMigrationFiles(dir)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT '')`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	applied := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			_ = rows.Close()
			return err
		}
		applied[v] = true
	}
	_ = rows.Close()

	for _, f := range files {
		if applied[f.version] {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, f.path)
		if err != nil {
			return fmt.Errorf("read %s: %w", f.path, err)
		}
		if _, err := s.db.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", f.version, err)
		}
		if _, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO schema_migrations (version, applied_at) VALUES ($1, $2)`), f.version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record %s: %w", f.version, err)
		}
	}
	return nil
}

type migrationFile struct {
	version string
	path    string
}

func listMigrationFiles(dir string) ([]migrationFile, error) {
	entries, err := fs.ReadDir(migrationsFS, dir)
	if err != nil {
		return nil, err
	}
	var ups []migrationFile
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		ups = append(ups, migrationFile{
			version: strings.TrimSuffix(name, ".up.sql"),
			path:    path.Join(dir, name),
		})
	}
	sort.Slice(ups, func(i, j int) bool { return ups[i].version < ups[j].version })
	return ups, nil
}

// --- Buckets ---

func (s *sqlStore) CreateBucket(ctx context.Context, tenant, bucket string) error {
	tenant = defaultTenant(tenant)
	var q string
	if s.dialect == dialectPostgres {
		q = `INSERT INTO buckets (tenant_id, name) VALUES ($1, $2) ON CONFLICT (tenant_id, name) DO NOTHING`
	} else {
		q = `INSERT OR IGNORE INTO buckets (tenant_id, name) VALUES ($1, $2)`
	}
	_, err := s.db.ExecContext(ctx, s.rebind(q), tenant, bucket)
	return err
}

func (s *sqlStore) BucketExists(ctx context.Context, tenant, bucket string) (bool, error) {
	tenant = defaultTenant(tenant)
	var count int
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT COUNT(1) FROM buckets WHERE tenant_id = $1 AND name = $2`), tenant, bucket)
	if err := row.Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListBuckets returns the names of all buckets owned by the tenant. Order is
// not significant; an empty (non-nil) slice is returned when the tenant has none.
func (s *sqlStore) ListBuckets(ctx context.Context, tenant string) ([]string, error) {
	tenant = defaultTenant(tenant)
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT name FROM buckets WHERE tenant_id = $1`), tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// --- Objects ---

func (s *sqlStore) UpsertObject(ctx context.Context, obj Object) (Object, error) {
	obj.TenantID = defaultTenant(obj.TenantID)
	if obj.VersionID == "" {
		obj.VersionID = newVersionID()
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
INSERT INTO objects (tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, created_at, updated_at, deleted_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb, now(), now(), NULL)
ON CONFLICT (tenant_id, bucket, key) WHERE deleted_at IS NULL
DO UPDATE SET backend=EXCLUDED.backend, storage_key=EXCLUDED.storage_key, size=EXCLUDED.size, etag=EXCLUDED.etag,
              content_type=EXCLUDED.content_type, metadata=EXCLUDED.metadata, tags=EXCLUDED.tags,
              version_id=EXCLUDED.version_id, updated_at=now()
RETURNING id, version_id, created_at, updated_at`
		args = []any{obj.TenantID, obj.Bucket, obj.Key, obj.VersionID, obj.Backend, obj.StorageKey, obj.Size, obj.ETag, obj.ContentType, string(metaBytes), string(tagBytes)}
	default:
		q = `
INSERT INTO objects (tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, created_at, updated_at, deleted_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, $12, $13, NULL)
ON CONFLICT (tenant_id, bucket, key) WHERE deleted_at IS NULL
DO UPDATE SET backend=excluded.backend, storage_key=excluded.storage_key, size=excluded.size, etag=excluded.etag,
              content_type=excluded.content_type, metadata=excluded.metadata, tags=excluded.tags,
              version_id=excluded.version_id, updated_at=excluded.updated_at
RETURNING id, version_id, created_at, updated_at`
		args = []any{obj.TenantID, obj.Bucket, obj.Key, obj.VersionID, obj.Backend, obj.StorageKey, obj.Size, obj.ETag, obj.ContentType, string(metaBytes), string(tagBytes), now, now}
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

// InsertObjectVersion soft-deletes the current active row (if any) and inserts
// a fresh row with a new version_id. Used for buckets with versioning enabled
// — preserves the previous row's storage_key for ?version= lookups.
func (s *sqlStore) InsertObjectVersion(ctx context.Context, obj Object) (Object, error) {
	obj.TenantID = defaultTenant(obj.TenantID)
	if obj.VersionID == "" {
		obj.VersionID = newVersionID()
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
	// Soft-delete prior active row (if any).
	if s.dialect == dialectPostgres {
		_, err = tx.ExecContext(ctx, `UPDATE objects SET deleted_at=now() WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NULL`,
			obj.TenantID, obj.Bucket, obj.Key)
	} else {
		_, err = tx.ExecContext(ctx, s.rebind(`UPDATE objects SET deleted_at=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
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
		q = `INSERT INTO objects (tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, created_at, updated_at, deleted_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb, now(), now(), NULL)
RETURNING id, version_id, created_at, updated_at`
		args = []any{obj.TenantID, obj.Bucket, obj.Key, obj.VersionID, obj.Backend, obj.StorageKey, obj.Size, obj.ETag, obj.ContentType, string(metaBytes), string(tagBytes)}
	} else {
		q = `INSERT INTO objects (tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, created_at, updated_at, deleted_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, $12, $13, NULL)
RETURNING id, version_id, created_at, updated_at`
		args = []any{obj.TenantID, obj.Bucket, obj.Key, obj.VersionID, obj.Backend, obj.StorageKey, obj.Size, obj.ETag, obj.ContentType, string(metaBytes), string(tagBytes), now, now}
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

func newVersionID() string {
	return uuidLike()
}

func (s *sqlStore) GetObject(ctx context.Context, tenant, bucket, key string) (Object, error) {
	tenant = defaultTenant(tenant)
	row := s.db.QueryRowContext(ctx, s.rebind(`
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, created_at, updated_at, deleted_at, locked_until
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
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, created_at, updated_at, deleted_at, locked_until
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

func (s *sqlStore) ListObjects(ctx context.Context, tenant, bucket, prefix, marker string, limit int) (ListPage, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	q := `
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, created_at, updated_at, deleted_at, locked_until
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

func (s *sqlStore) SoftDeleteObject(ctx context.Context, tenant, bucket, key string) error {
	tenant = defaultTenant(tenant)
	res, err := s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET deleted_at=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
		time.Now().UTC().Format(time.RFC3339Nano), tenant, bucket, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqlStore) HardDeleteObject(ctx context.Context, tenant, bucket, key string) error {
	tenant = defaultTenant(tenant)
	_, err := s.db.ExecContext(ctx, s.rebind(`DELETE FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3`), tenant, bucket, key)
	return err
}

func (s *sqlStore) GetObjectVersion(ctx context.Context, tenant, bucket, key, versionID string) (Object, error) {
	tenant = defaultTenant(tenant)
	row := s.db.QueryRowContext(ctx, s.rebind(`
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, created_at, updated_at, deleted_at, locked_until
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
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, created_at, updated_at, deleted_at, locked_until
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

func (s *sqlStore) UpdateTags(ctx context.Context, tenant, bucket, key string, tags map[string]string) error {
	tenant = defaultTenant(tenant)
	tagBytes, err := jsonOrEmpty(tags)
	if err != nil {
		return err
	}
	var q string
	if s.dialect == dialectPostgres {
		q = `UPDATE objects SET tags=$1::jsonb, updated_at=now() WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`
	} else {
		q = `UPDATE objects SET tags=$1, updated_at=$2 WHERE tenant_id=$3 AND bucket=$4 AND key=$5 AND deleted_at IS NULL`
	}
	var args []any
	if s.dialect == dialectPostgres {
		args = []any{string(tagBytes), tenant, bucket, key}
	} else {
		args = []any{string(tagBytes), time.Now().UTC().Format(time.RFC3339Nano), tenant, bucket, key}
	}
	res, err := s.db.ExecContext(ctx, s.rebind(q), args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqlStore) SetLockedUntil(ctx context.Context, tenant, bucket, key string, until time.Time) error {
	tenant = defaultTenant(tenant)
	var q string
	if s.dialect == dialectPostgres {
		q = `UPDATE objects SET locked_until=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`
	} else {
		q = `UPDATE objects SET locked_until=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`
	}
	res, err := s.db.ExecContext(ctx, s.rebind(q), until.UTC().Format(time.RFC3339Nano), tenant, bucket, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// StorageKeyReferenced reports whether any object row references the given
// storage key. Soft-deleted rows still pin their blob, so deleted_at is not
// filtered here.
func (s *sqlStore) StorageKeyReferenced(ctx context.Context, storageKey string) (bool, error) {
	var one int
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT 1 FROM objects WHERE storage_key = $1 LIMIT 1`), storageKey)
	if err := row.Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *sqlStore) GetBucketConfig(ctx context.Context, tenant, bucket string) (BucketConfig, error) {
	tenant = defaultTenant(tenant)
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT tenant_id, name, versioning, object_lock_seconds, expire_after_days, expire_action, acl FROM buckets WHERE tenant_id=$1 AND name=$2`), tenant, bucket)
	var cfg BucketConfig
	var versioning sql.NullBool
	var acl sql.NullString
	if err := row.Scan(&cfg.TenantID, &cfg.Name, &versioning, &cfg.ObjectLockSeconds, &cfg.ExpireAfterDays, &cfg.ExpireAction, &acl); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BucketConfig{TenantID: tenant, Name: bucket}, nil
		}
		return BucketConfig{}, err
	}
	cfg.Versioning = versioning.Bool
	cfg.ACL = acl.String
	return cfg, nil
}

// SetBucketACL sets a bucket's canned ACL (creating the bucket row if needed).
func (s *sqlStore) SetBucketACL(ctx context.Context, tenant, bucket, acl string) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET acl=$1 WHERE tenant_id=$2 AND name=$3`), acl, tenant, bucket)
	return err
}

// SetObjectACL sets an object's canned ACL.
func (s *sqlStore) SetObjectACL(ctx context.Context, tenant, bucket, key, acl string) error {
	tenant = defaultTenant(tenant)
	res, err := s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET acl=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`), acl, tenant, bucket, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetObjectACL returns an object's effective canned ACL.
func (s *sqlStore) GetObjectACL(ctx context.Context, tenant, bucket, key string) (string, error) {
	tenant = defaultTenant(tenant)
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT acl FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NULL`), tenant, bucket, key)
	var acl sql.NullString
	if err := row.Scan(&acl); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if acl.String == "" {
		return "private", nil
	}
	return acl.String, nil
}

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

// ListExpired finds active objects whose bucket has expire_after_days > 0 and
// whose updated_at is older than that window. limit caps the batch.
func (s *sqlStore) ListExpired(ctx context.Context, limit int) ([]Object, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `
SELECT o.id, o.tenant_id, o.bucket, o.key, o.version_id, o.backend, o.storage_key, o.size, o.etag, o.content_type,
       o.metadata, o.tags, o.created_at, o.updated_at, o.deleted_at, o.locked_until
FROM objects o JOIN buckets b ON o.tenant_id = b.tenant_id AND o.bucket = b.name
WHERE o.deleted_at IS NULL AND b.expire_after_days > 0
  AND o.updated_at < $1
LIMIT $2`
	// cutoff = now - max(expire_after_days). We let SQL filter per-row instead.
	// Simpler portable form: pull rows where any non-zero expire_after_days
	// applies and filter the cutoff in Go.
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT o.id, o.tenant_id, o.bucket, o.key, o.version_id, o.backend, o.storage_key, o.size, o.etag, o.content_type,
       o.metadata, o.tags, o.created_at, o.updated_at, o.deleted_at, o.locked_until,
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
			&obj.Size, &obj.ETag, &obj.ContentType, &metaRaw, &tagRaw, &created, &updated, &deleted, &locked,
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
		// emit only those past the per-bucket cutoff.
		if updated.Time.Add(time.Duration(days) * 24 * time.Hour).Before(now) {
			if action != "" {
				obj.Metadata["__expire_action"] = action
			}
			out = append(out, obj)
		}
		_ = q
	}
	return out, rows.Err()
}

// ListSoftDeletedBefore finds objects that were soft-deleted (deleted_at set)
// strictly before `before` (an RFC3339 timestamp), oldest first, up to limit.
// It is the input to the retention GC, which permanently purges these rows and
// their backing blobs. Returns a non-nil empty slice when none match.
func (s *sqlStore) ListSoftDeletedBefore(ctx context.Context, before string, limit int) ([]Object, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT id, tenant_id, bucket, key, version_id, backend, storage_key, size, etag, content_type, metadata, tags, created_at, updated_at, deleted_at, locked_until
FROM objects
WHERE deleted_at IS NOT NULL AND deleted_at < $1
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

func (s *sqlStore) SetBucketVersioning(ctx context.Context, tenant, bucket string, enabled bool) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	v := 0
	if enabled {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET versioning=$1 WHERE tenant_id=$2 AND name=$3`), v, tenant, bucket)
	return err
}

func (s *sqlStore) SetBucketObjectLock(ctx context.Context, tenant, bucket string, seconds int) error {
	tenant = defaultTenant(tenant)
	if err := s.CreateBucket(ctx, tenant, bucket); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE buckets SET object_lock_seconds=$1 WHERE tenant_id=$2 AND name=$3`), seconds, tenant, bucket)
	return err
}

// --- Multipart ---

func (s *sqlStore) CreateUpload(ctx context.Context, u Upload) error {
	u.TenantID = defaultTenant(u.TenantID)
	metaBytes, err := jsonOrEmpty(u.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.rebind(`INSERT INTO multipart_uploads (upload_id, tenant_id, bucket, key, backend, backend_uid, storage_key, metadata) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`),
		u.ID, u.TenantID, u.Bucket, u.Key, u.Backend, u.BackendUID, u.StorageKey, string(metaBytes))
	return err
}

func (s *sqlStore) GetUpload(ctx context.Context, uploadID string) (Upload, error) {
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT upload_id, tenant_id, bucket, key, backend, backend_uid, storage_key, metadata, created_at FROM multipart_uploads WHERE upload_id=$1`), uploadID)
	var (
		u        Upload
		metaRaw  []byte
		createdT flexTime
	)
	if err := row.Scan(&u.ID, &u.TenantID, &u.Bucket, &u.Key, &u.Backend, &u.BackendUID, &u.StorageKey, &metaRaw, &createdT); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Upload{}, ErrUploadNotFound
		}
		return Upload{}, err
	}
	u.CreatedAt = createdT.Time
	u.Metadata, _ = unmarshalKV(metaRaw)
	return u, nil
}

func (s *sqlStore) DeleteUpload(ctx context.Context, uploadID string) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`DELETE FROM multipart_uploads WHERE upload_id=$1`), uploadID)
	return err
}

// ListUploads returns in-progress multipart uploads for a tenant, optionally
// filtered by bucket (empty = all buckets). Used by S3 ListMultipartUploads.
func (s *sqlStore) ListUploads(ctx context.Context, tenant, bucket string, limit int) ([]Upload, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	q := `SELECT upload_id, tenant_id, bucket, key, backend, backend_uid, storage_key, metadata, created_at FROM multipart_uploads WHERE tenant_id=$1`
	args := []any{tenant}
	if bucket != "" {
		q += ` AND bucket=$2 ORDER BY created_at ASC LIMIT $3`
		args = append(args, bucket, limit)
	} else {
		q += ` ORDER BY created_at ASC LIMIT $2`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Upload
	for rows.Next() {
		var (
			u        Upload
			metaRaw  []byte
			createdT flexTime
		)
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Bucket, &u.Key, &u.Backend, &u.BackendUID, &u.StorageKey, &metaRaw, &createdT); err != nil {
			return nil, err
		}
		u.CreatedAt = createdT.Time
		u.Metadata, _ = unmarshalKV(metaRaw)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *sqlStore) RecordPart(ctx context.Context, p PartRecord) error {
	var q string
	if s.dialect == dialectPostgres {
		q = `INSERT INTO multipart_parts (upload_id, part_number, etag, size) VALUES ($1,$2,$3,$4)
ON CONFLICT (upload_id, part_number) DO UPDATE SET etag=EXCLUDED.etag, size=EXCLUDED.size`
	} else {
		q = `INSERT INTO multipart_parts (upload_id, part_number, etag, size) VALUES ($1,$2,$3,$4)
ON CONFLICT (upload_id, part_number) DO UPDATE SET etag=excluded.etag, size=excluded.size`
	}
	_, err := s.db.ExecContext(ctx, s.rebind(q), p.UploadID, p.PartNumber, p.ETag, p.Size)
	return err
}

func (s *sqlStore) ListParts(ctx context.Context, uploadID string) ([]PartRecord, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT upload_id, part_number, etag, size FROM multipart_parts WHERE upload_id=$1 ORDER BY part_number ASC`), uploadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PartRecord
	for rows.Next() {
		var p PartRecord
		if err := rows.Scan(&p.UploadID, &p.PartNumber, &p.ETag, &p.Size); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- Events ---

func (s *sqlStore) InsertEvent(ctx context.Context, e Event) (int64, error) {
	e.TenantID = defaultTenant(e.TenantID)
	payload, err := jsonOrEmpty(e.Payload)
	if err != nil {
		return 0, err
	}
	var oid any = nil
	if e.ObjectID != nil {
		oid = *e.ObjectID
	}
	var (
		q   string
		row *sql.Row
	)
	if s.dialect == dialectPostgres {
		q = `INSERT INTO object_events (tenant_id, bucket, key, type, object_id, request_id, payload) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb) RETURNING id`
	} else {
		q = `INSERT INTO object_events (tenant_id, bucket, key, type, object_id, request_id, payload) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`
	}
	row = s.db.QueryRowContext(ctx, s.rebind(q), e.TenantID, e.Bucket, e.Key, string(e.Type), oid, e.RequestID, string(payload))
	var id int64
	if err := row.Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *sqlStore) NextUnconsumedEvents(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 32
	}
	q := `SELECT id, tenant_id, bucket, key, type, object_id, request_id, payload, created_at
FROM object_events WHERE consumed_at IS NULL ORDER BY id ASC LIMIT $1`
	rows, err := s.db.QueryContext(ctx, s.rebind(q), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var (
			e        Event
			oid      sql.NullInt64
			payload  []byte
			createdT flexTime
			typeStr  string
		)
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Bucket, &e.Key, &typeStr, &oid, &e.RequestID, &payload, &createdT); err != nil {
			return nil, err
		}
		e.Type = EventType(typeStr)
		if oid.Valid {
			v := oid.Int64
			e.ObjectID = &v
		}
		e.CreatedAt = createdT.Time
		e.Payload, _ = unmarshalKV(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *sqlStore) MarkEventConsumed(ctx context.Context, id int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var q string
	if s.dialect == dialectPostgres {
		q = `UPDATE object_events SET consumed_at=now() WHERE id=$1`
		_, err := s.db.ExecContext(ctx, s.rebind(q), id)
		return err
	}
	q = `UPDATE object_events SET consumed_at=$1 WHERE id=$2`
	_, err := s.db.ExecContext(ctx, s.rebind(q), now, id)
	return err
}

// --- Chunks ---

func (s *sqlStore) DeleteChunksForObject(ctx context.Context, objectID int64) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`DELETE FROM chunks WHERE object_id=$1`), objectID)
	return err
}

func (s *sqlStore) InsertChunks(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.rebind(`INSERT INTO chunks (object_id, tenant_id, bucket, object_key, seq, content, embedding, dim, embed_model) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`)
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range chunks {
		c.TenantID = defaultTenant(c.TenantID)
		blob := encodeEmbedding(c.Embedding)
		if _, err := stmt.ExecContext(ctx, c.ObjectID, c.TenantID, c.Bucket, c.ObjectKey, c.Seq, c.Content, blob, c.Dim, c.EmbedModel); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *sqlStore) ListChunksForObject(ctx context.Context, objectID int64) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT id, object_id, tenant_id, bucket, object_key, seq, content, embedding, dim, embed_model, created_at FROM chunks WHERE object_id=$1 ORDER BY seq ASC`), objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunks(rows)
}

// ListObjectIDsToReindex returns distinct object IDs whose chunks were embedded
// by a model other than currentModel — i.e. stale after the embedder changed —
// so they can be re-indexed against the active model.
func (s *sqlStore) ListObjectIDsToReindex(ctx context.Context, tenant, currentModel string, limit int) ([]int64, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT DISTINCT object_id FROM chunks WHERE tenant_id=$1 AND embed_model <> '' AND embed_model <> $2 LIMIT $3`),
		tenant, currentModel, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *sqlStore) SearchChunks(ctx context.Context, tenant, bucket string, query []float32, limit int) ([]SearchHit, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	// Brute-force cosine: load filtered chunks and rank in-process.
	// Trade-off: simple + portable across SQLite/PG, scales to ~100K chunks per tenant.
	q := `SELECT id, object_id, tenant_id, bucket, object_key, seq, content, embedding, dim, embed_model, created_at
FROM chunks WHERE tenant_id=$1 AND embedding IS NOT NULL`
	args := []any{tenant}
	if bucket != "" {
		q += ` AND bucket=$2`
		args = append(args, bucket)
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all, err := scanChunks(rows)
	if err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, len(all))
	qNorm := norm(query)
	for _, c := range all {
		if len(c.Embedding) != len(query) || qNorm == 0 {
			continue
		}
		score := cosine(query, c.Embedding, qNorm)
		hits = append(hits, SearchHit{Chunk: c, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// --- Audit ---

func (s *sqlStore) RecordUsage(ctx context.Context, u Usage) error {
	u.TenantID = defaultTenant(u.TenantID)
	cidBytes, _ := json.Marshal(u.ChunkIDs)
	oidBytes, _ := json.Marshal(u.ObjectIDs)
	var q string
	if s.dialect == dialectPostgres {
		q = `INSERT INTO ai_usage (tenant_id, caller, query, chunk_ids, object_ids, request_id, model, prompt_tokens, completion_tokens, total_tokens, latency_ms, cost_micros) VALUES ($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8,$9,$10,$11,$12)`
	} else {
		q = `INSERT INTO ai_usage (tenant_id, caller, query, chunk_ids, object_ids, request_id, model, prompt_tokens, completion_tokens, total_tokens, latency_ms, cost_micros) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`
	}
	_, err := s.db.ExecContext(ctx, s.rebind(q), u.TenantID, u.Caller, u.Query, string(cidBytes), string(oidBytes), u.RequestID, u.Model, u.PromptTokens, u.CompletionTokens, u.TotalTokens, u.LatencyMs, u.CostMicros)
	return err
}

func (s *sqlStore) SumAICostMicros(ctx context.Context, tenant, since string) (int64, error) {
	tenant = defaultTenant(tenant)
	q := `SELECT COALESCE(SUM(cost_micros), 0) FROM ai_usage WHERE tenant_id = $1 AND created_at >= $2`
	var total int64
	if err := s.db.QueryRowContext(ctx, s.rebind(q), tenant, since).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (s *sqlStore) ListUsageForObject(ctx context.Context, tenant string, objectID int64, limit int) ([]Usage, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	// Filter rows where object_ids JSON contains the given id.
	var q string
	if s.dialect == dialectPostgres {
		q = `SELECT id, tenant_id, caller, query, chunk_ids, object_ids, request_id, created_at, model, prompt_tokens, completion_tokens, total_tokens, latency_ms, cost_micros
FROM ai_usage WHERE tenant_id=$1 AND object_ids @> $2::jsonb ORDER BY id DESC LIMIT $3`
		oidJSON := fmt.Sprintf("[%d]", objectID)
		rows, err := s.db.QueryContext(ctx, s.rebind(q), tenant, oidJSON, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanUsages(rows)
	}
	q = `SELECT id, tenant_id, caller, query, chunk_ids, object_ids, request_id, created_at, model, prompt_tokens, completion_tokens, total_tokens, latency_ms, cost_micros
FROM ai_usage WHERE tenant_id=$1 ORDER BY id DESC LIMIT $2`
	rows, err := s.db.QueryContext(ctx, s.rebind(q), tenant, limit*4)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all, err := scanUsages(rows)
	if err != nil {
		return nil, err
	}
	out := make([]Usage, 0, limit)
	for _, u := range all {
		for _, id := range u.ObjectIDs {
			if id == objectID {
				out = append(out, u)
				break
			}
		}
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

// --- Scanners ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanObject(row rowScanner) (Object, error) {
	var (
		obj      Object
		metaRaw  []byte
		tagRaw   []byte
		createdT flexTime
		updatedT flexTime
		deletedT flexNullTime
		lockedT  flexNullTime
	)
	err := row.Scan(
		&obj.ID, &obj.TenantID, &obj.Bucket, &obj.Key, &obj.VersionID, &obj.Backend, &obj.StorageKey,
		&obj.Size, &obj.ETag, &obj.ContentType,
		&metaRaw, &tagRaw,
		&createdT, &updatedT, &deletedT, &lockedT,
	)
	if err != nil {
		return Object{}, err
	}
	obj.Metadata, _ = unmarshalKV(metaRaw)
	obj.Tags, _ = unmarshalKV(tagRaw)
	obj.CreatedAt = createdT.Time
	obj.UpdatedAt = updatedT.Time
	if deletedT.Valid {
		t := deletedT.Time
		obj.DeletedAt = &t
	}
	if lockedT.Valid {
		t := lockedT.Time
		obj.LockedUntil = &t
	}
	return obj, nil
}

// uuidLike returns a 36-char hex string; we keep it dep-free here since the
// rest of the package only needs uniqueness, not RFC-4122 compliance.
func uuidLike() string {
	const hex = "0123456789abcdef"
	now := time.Now().UnixNano()
	// 16 bytes of pseudo-random material seeded from time + crypto/rand.
	var b [16]byte
	_, _ = cryptoRandRead(b[:])
	// Mix in time so collisions across cold-starts are rare even if rand fails.
	binaryPutUint64(b[:8], uint64(now))
	var out [36]byte
	idx := 0
	for i, x := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out[idx] = '-'
			idx++
		}
		out[idx] = hex[x>>4]
		out[idx+1] = hex[x&0x0f]
		idx += 2
	}
	return string(out[:idx])
}

func scanChunks(rows *sql.Rows) ([]Chunk, error) {
	var out []Chunk
	for rows.Next() {
		var (
			c        Chunk
			embedRaw []byte
			createdT flexTime
		)
		if err := rows.Scan(&c.ID, &c.ObjectID, &c.TenantID, &c.Bucket, &c.ObjectKey, &c.Seq, &c.Content, &embedRaw, &c.Dim, &c.EmbedModel, &createdT); err != nil {
			return nil, err
		}
		c.CreatedAt = createdT.Time
		c.Embedding = decodeEmbedding(embedRaw)
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanUsages(rows *sql.Rows) ([]Usage, error) {
	var out []Usage
	for rows.Next() {
		var (
			u        Usage
			cidRaw   []byte
			oidRaw   []byte
			createdT flexTime
		)
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Caller, &u.Query, &cidRaw, &oidRaw, &u.RequestID, &createdT, &u.Model, &u.PromptTokens, &u.CompletionTokens, &u.TotalTokens, &u.LatencyMs, &u.CostMicros); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(cidRaw, &u.ChunkIDs)
		_ = json.Unmarshal(oidRaw, &u.ObjectIDs)
		u.CreatedAt = createdT.Time
		out = append(out, u)
	}
	return out, rows.Err()
}

// --- Helpers ---

func defaultTenant(t string) string {
	if t == "" {
		return "default"
	}
	return t
}

func unmarshalKV(b []byte) (map[string]string, error) {
	if len(b) == 0 {
		return map[string]string{}, nil
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func jsonOrEmpty(m map[string]string) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// encodeEmbedding packs []float32 as little-endian bytes; nil → nil so the
// column can stay NULL until an embedder runs.
func encodeEmbedding(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func decodeEmbedding(b []byte) []float32 {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

func norm(v []float32) float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return float32(math.Sqrt(s))
}

func cosine(a, b []float32, aNorm float32) float32 {
	var dot, bSq float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		bSq += float64(b[i]) * float64(b[i])
	}
	bNorm := math.Sqrt(bSq)
	if bNorm == 0 || aNorm == 0 {
		return 0
	}
	return float32(dot / (float64(aNorm) * bNorm))
}

// flexTime accepts time.Time, []byte, or string (RFC3339[Nano]).
type flexTime struct {
	Time time.Time
}

func (t *flexTime) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		return nil
	case time.Time:
		t.Time = v
	case []byte:
		return t.parse(string(v))
	case string:
		return t.parse(v)
	default:
		return fmt.Errorf("flexTime: unsupported %T", src)
	}
	return nil
}

func (t *flexTime) parse(s string) error {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed
			return nil
		}
	}
	if ns, err := strconv.ParseInt(s, 10, 64); err == nil {
		t.Time = time.Unix(0, ns)
		return nil
	}
	return fmt.Errorf("flexTime: cannot parse %q", s)
}

type flexNullTime struct {
	Time  time.Time
	Valid bool
}

func (t *flexNullTime) Scan(src any) error {
	if src == nil {
		return nil
	}
	var f flexTime
	if err := f.Scan(src); err != nil {
		return err
	}
	t.Time = f.Time
	t.Valid = !f.Time.IsZero()
	return nil
}

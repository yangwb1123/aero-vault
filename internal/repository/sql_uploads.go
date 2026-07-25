package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

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
// filtered by bucket (empty = all buckets). Results are ordered by (key,
// upload_id) and paged past (keyMarker, uploadIDMarker) for AWS-style
// ListMultipartUploads pagination. Used by S3 ListMultipartUploads.
func (s *sqlStore) ListUploads(ctx context.Context, tenant, bucket, keyMarker, uploadIDMarker string, limit int) ([]Upload, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 {
		limit = 1000
	}
	if limit > 10000 {
		limit = 10000
	}
	q := `SELECT upload_id, tenant_id, bucket, key, backend, backend_uid, storage_key, metadata, created_at FROM multipart_uploads WHERE tenant_id=$1`
	args := []any{tenant}
	n := 1
	if bucket != "" {
		n++
		q += fmt.Sprintf(" AND bucket=$%d", n)
		args = append(args, bucket)
	}
	if keyMarker != "" {
		a, b, c := n+1, n+2, n+3
		q += fmt.Sprintf(" AND (key > $%d OR (key = $%d AND upload_id > $%d))", a, b, c)
		args = append(args, keyMarker, keyMarker, uploadIDMarker)
		n += 3
	}
	n++
	q += fmt.Sprintf(" ORDER BY key ASC, upload_id ASC LIMIT $%d", n)
	args = append(args, limit)
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

// ListExpiredUploads returns multipart uploads whose created_at is before the
// given RFC3339 timestamp, used by the upload GC sweep to find stale uploads.
func (s *sqlStore) ListExpiredUploads(ctx context.Context, before string, limit int) ([]Upload, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT upload_id, tenant_id, bucket, key, backend, backend_uid, storage_key, metadata, created_at FROM multipart_uploads WHERE created_at < $1 ORDER BY created_at LIMIT $2`), before, limit)
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

// DeleteUploadCascade deletes all parts for an upload, then the upload record
// itself, in a single transaction. Returns ErrUploadNotFound when the upload
// does not exist.
func (s *sqlStore) DeleteUploadCascade(ctx context.Context, uploadID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Verify the upload exists.
	var count int
	if err := tx.QueryRowContext(ctx, s.rebind(`SELECT COUNT(1) FROM multipart_uploads WHERE upload_id=$1`), uploadID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrUploadNotFound
	}

	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM multipart_parts WHERE upload_id=$1`), uploadID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM multipart_uploads WHERE upload_id=$1`), uploadID); err != nil {
		return err
	}
	return tx.Commit()
}

// ListZombieUploads returns uploads that have at least one part recorded but
// were never completed and are older than `before`. These are uploads where the
// client uploaded parts but then disconnected without calling CompleteMultipart
// or AbortMultipart.
func (s *sqlStore) ListZombieUploads(ctx context.Context, before string, limit int) ([]Upload, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT u.upload_id, u.tenant_id, u.bucket, u.key, u.backend, u.backend_uid, u.storage_key, u.metadata, u.created_at
FROM multipart_uploads u
WHERE u.created_at < $1
  AND EXISTS (SELECT 1 FROM multipart_parts p WHERE p.upload_id = u.upload_id)
ORDER BY u.created_at
LIMIT $2`), before, limit)
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

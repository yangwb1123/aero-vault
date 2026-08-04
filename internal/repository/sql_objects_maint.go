package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ── Storage Class / Delete / Restore ──────────────────────────────────────────

// UpdateObjectStorageClass changes the storage_class of a single active object.
func (s *sqlStore) UpdateObjectStorageClass(ctx context.Context, tenant, bucket, key, storageClass string) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET storage_class=$1, updated_at=$2 WHERE tenant_id=$3 AND bucket=$4 AND key=$5 AND deleted_at IS NULL`),
		storageClass, time.Now().UTC().Format(time.RFC3339Nano), tenant, bucket, key)
	return err
}

func (s *sqlStore) SoftDeleteObject(ctx context.Context, tenant, bucket, key string) error {
	tenant = defaultTenant(tenant)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, s.rebind(`UPDATE objects SET deleted_at=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
		time.Now().UTC().Format(time.RFC3339Nano), tenant, bucket, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if err := deleteObjectAccessState(ctx, s, tx, tenant, bucket, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqlStore) SoftDeleteObjectByID(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var tenant, bucket, key string
	if err := tx.QueryRowContext(ctx, s.rebind(
		`SELECT tenant_id, bucket, key FROM objects WHERE id=$1 AND deleted_at IS NULL`,
	), id).Scan(&tenant, &bucket, &key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	res, err := tx.ExecContext(
		ctx,
		s.rebind(`UPDATE objects SET deleted_at=$1 WHERE id=$2 AND deleted_at IS NULL`),
		time.Now().UTC().Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := deleteObjectAccessState(ctx, s, tx, tenant, bucket, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqlStore) HardDeleteObject(ctx context.Context, tenant, bucket, key string) error {
	tenant = defaultTenant(tenant)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var held bool
	if err := tx.QueryRowContext(ctx, s.rebind(`
SELECT EXISTS (SELECT 1 FROM objects o
  JOIN legal_holds h ON h.object_id = o.id
  WHERE o.tenant_id=$1 AND o.bucket=$2 AND o.key=$3)`), tenant, bucket, key).Scan(&held); err != nil {
		return err
	}
	if held {
		return ErrLegalHoldActive
	}
	if err := deleteObjectAccessState(ctx, s, tx, tenant, bucket, key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3`), tenant, bucket, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqlStore) HardDeleteObjectByID(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var tenant, bucket, key string
	if err := tx.QueryRowContext(ctx, s.rebind(
		`SELECT tenant_id, bucket, key FROM objects WHERE id=$1`,
	), id).Scan(&tenant, &bucket, &key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var held bool
	if err := tx.QueryRowContext(ctx, s.rebind(`SELECT EXISTS (SELECT 1 FROM legal_holds WHERE object_id=$1)`), id).Scan(&held); err != nil {
		return err
	}
	if held {
		return ErrLegalHoldActive
	}
	res, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM objects WHERE id=$1`), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	remaining, err := objectVersionCount(ctx, s, tx, tenant, bucket, key)
	if err != nil {
		return err
	}
	if remaining == 0 {
		if err := deleteObjectAccessState(ctx, s, tx, tenant, bucket, key); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteObjectVersion removes one exact version and promotes the newest
// remaining row when the deleted row was the current version.
func (s *sqlStore) DeleteObjectVersion(ctx context.Context, tenant, bucket, key, versionID string) error {
	tenant = defaultTenant(tenant)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var objectID int64
	var wasCurrent int
	if err := tx.QueryRowContext(ctx, s.rebind(`
SELECT id, CASE WHEN deleted_at IS NULL THEN 1 ELSE 0 END FROM objects
WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND version_id=$4`),
		tenant, bucket, key, versionID).Scan(&objectID, &wasCurrent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var held bool
	if err := tx.QueryRowContext(ctx, s.rebind(
		`SELECT EXISTS (SELECT 1 FROM legal_holds WHERE object_id=$1)`,
	), objectID).Scan(&held); err != nil {
		return err
	}
	if held {
		return ErrLegalHoldActive
	}
	if wasCurrent != 0 {
		if err := deleteObjectAccessState(ctx, s, tx, tenant, bucket, key); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM objects WHERE id=$1`), objectID); err != nil {
		return err
	}
	if err := promoteLatestObjectVersion(ctx, s, tx, tenant, bucket, key); err != nil {
		return err
	}
	remaining, err := objectVersionCount(ctx, s, tx, tenant, bucket, key)
	if err != nil {
		return err
	}
	if remaining == 0 {
		if err := deleteObjectAccessState(ctx, s, tx, tenant, bucket, key); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func objectVersionCount(
	ctx context.Context, store *sqlStore, tx *sql.Tx, tenant, bucket, key string,
) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, store.rebind(
		`SELECT COUNT(1) FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3`,
	), tenant, bucket, key).Scan(&count)
	return count, err
}

func promoteLatestObjectVersion(
	ctx context.Context, store *sqlStore, tx *sql.Tx, tenant, bucket, key string,
) error {
	var active int
	if err := tx.QueryRowContext(ctx, store.rebind(`
SELECT COUNT(1) FROM objects
WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NULL`),
		tenant, bucket, key).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return nil
	}
	var (
		objectID int64
		metaRaw  []byte
	)
	err := tx.QueryRowContext(ctx, store.rebind(`
SELECT id, metadata FROM objects
WHERE tenant_id=$1 AND bucket=$2 AND key=$3
ORDER BY updated_at DESC, id DESC LIMIT 1`), tenant, bucket, key).Scan(&objectID, &metaRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	metadata, _ := unmarshalKV(metaRaw)
	if metadata["_aero_delete_marker"] == "true" {
		return nil
	}
	_, err = tx.ExecContext(ctx, store.rebind(
		`UPDATE objects SET deleted_at=NULL, version_tombstone=$1 WHERE id=$2`,
	), false, objectID)
	return err
}

func (s *sqlStore) RestoreObject(ctx context.Context, tenant, bucket, key string) error {
	tenant = defaultTenant(tenant)
	res, err := s.db.ExecContext(ctx, s.rebind(`
UPDATE objects SET deleted_at=NULL
WHERE id = (
  SELECT id FROM objects
  WHERE tenant_id=$1 AND bucket=$2 AND key=$3
    AND deleted_at IS NOT NULL AND version_tombstone=$4
  ORDER BY updated_at DESC
  LIMIT 1
) AND NOT EXISTS (
  SELECT 1 FROM objects
  WHERE tenant_id=$5 AND bucket=$6 AND key=$7 AND deleted_at IS NULL
)`),
		tenant, bucket, key, false,
		tenant, bucket, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Metadata operations ─────────────────────────────────────────────────────

func (s *sqlStore) SetObjectMetaKey(ctx context.Context, tenant, bucket, key, metaKey, metaValue string) error {
	tenant = defaultTenant(tenant)
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT metadata FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NULL`), tenant, bucket, key)
	var rawMeta []byte
	if err := row.Scan(&rawMeta); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	meta, _ := unmarshalKV(rawMeta)
	if meta == nil {
		meta = map[string]string{}
	}
	meta[metaKey] = metaValue
	updated, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if s.dialect == dialectPostgres {
		_, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata=$1::jsonb WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(updated), tenant, bucket, key)
	} else {
		_, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(updated), tenant, bucket, key)
	}
	return err
}

func (s *sqlStore) SetObjectMetaKeys(ctx context.Context, tenant, bucket, key string, meta map[string]string) error {
	if len(meta) == 0 {
		return nil
	}
	tenant = defaultTenant(tenant)
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT metadata FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NULL`), tenant, bucket, key)
	var rawMeta []byte
	if err := row.Scan(&rawMeta); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	current, _ := unmarshalKV(rawMeta)
	if current == nil {
		current = map[string]string{}
	}
	for k, v := range meta {
		current[k] = v
	}
	updated, err := json.Marshal(current)
	if err != nil {
		return err
	}
	if s.dialect == dialectPostgres {
		_, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata=$1::jsonb WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(updated), tenant, bucket, key)
	} else {
		_, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(updated), tenant, bucket, key)
	}
	return err
}

func (s *sqlStore) ReplaceObjectMetadata(ctx context.Context, tenant, bucket, key string, meta map[string]string) error {
	tenant = defaultTenant(tenant)
	updated, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	var result sql.Result
	if s.dialect == dialectPostgres {
		result, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata=$1::jsonb WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(updated), tenant, bucket, key)
	} else {
		result, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(updated), tenant, bucket, key)
	}
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqlStore) DeleteObjectMetaKey(ctx context.Context, tenant, bucket, key, metaKey string) error {
	tenant = defaultTenant(tenant)
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT metadata FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NULL`), tenant, bucket, key)
	var rawMeta []byte
	if err := row.Scan(&rawMeta); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	current, _ := unmarshalKV(rawMeta)
	if len(current) == 0 {
		return nil
	}
	delete(current, metaKey)
	updated, err := json.Marshal(current)
	if err != nil {
		return err
	}
	if s.dialect == dialectPostgres {
		_, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata=$1::jsonb WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(updated), tenant, bucket, key)
	} else {
		_, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(updated), tenant, bucket, key)
	}
	return err
}

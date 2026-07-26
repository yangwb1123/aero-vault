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
	_, err := s.db.ExecContext(ctx, s.rebind(`
DELETE FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3
  AND id NOT IN (SELECT object_id FROM legal_holds WHERE legal_holds.object_id = objects.id)`), tenant, bucket, key)
	if err != nil {
		return err
	}
	var held bool
	_ = s.db.QueryRowContext(ctx, s.rebind(`
SELECT EXISTS (SELECT 1 FROM objects o
  JOIN legal_holds h ON h.object_id = o.id
  WHERE o.tenant_id=$1 AND o.bucket=$2 AND o.key=$3)`), tenant, bucket, key).Scan(&held)
	if held {
		return ErrLegalHoldActive
	}
	return nil
}

func (s *sqlStore) HardDeleteObjectByID(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, s.rebind(`
DELETE FROM objects WHERE id=$1
  AND id NOT IN (SELECT object_id FROM legal_holds WHERE legal_holds.object_id = $1)`), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var held bool
		_ = s.db.QueryRowContext(ctx, s.rebind(`
SELECT EXISTS (SELECT 1 FROM legal_holds WHERE object_id=$1)`), id).Scan(&held)
		if held {
			return ErrLegalHoldActive
		}
		return ErrNotFound
	}
	return nil
}

func (s *sqlStore) RestoreObject(ctx context.Context, tenant, bucket, key string) error {
	tenant = defaultTenant(tenant)
	res, err := s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET deleted_at=NULL WHERE tenant_id=$1 AND bucket=$2 AND key=$3 AND deleted_at IS NOT NULL`),
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
	if s.dialect == dialectPostgres {
		_, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata=$1::jsonb WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(updated), tenant, bucket, key)
	} else {
		_, err = s.db.ExecContext(ctx, s.rebind(`UPDATE objects SET metadata=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
			string(updated), tenant, bucket, key)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
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

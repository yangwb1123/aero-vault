package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

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

// UpdateObjectTagsByID updates one exact version instead of whichever row is
// currently active for a key. Background jobs use this because a newer version
// may be uploaded between event publication and job execution.
func (s *sqlStore) UpdateObjectTagsByID(ctx context.Context, id int64, tags map[string]string) error {
	tagBytes, err := jsonOrEmpty(tags)
	if err != nil {
		return err
	}
	query := `UPDATE objects SET tags=$1, updated_at=$2 WHERE id=$3`
	args := []any{string(tagBytes), time.Now().UTC().Format(time.RFC3339Nano), id}
	if s.dialect == dialectPostgres {
		query = `UPDATE objects SET tags=$1::jsonb, updated_at=now() WHERE id=$2`
		args = []any{string(tagBytes), id}
	}
	result, err := s.db.ExecContext(ctx, s.rebind(query), args...)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
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

// SetObjectRetention updates one exact object version. Object IDs are used
// because historical versions share tenant/bucket/key with the current row.
func (s *sqlStore) SetObjectRetention(ctx context.Context, objectID int64, until time.Time, metadata map[string]string) error {
	metaBytes, err := jsonOrEmpty(metadata)
	if err != nil {
		return err
	}
	query := `UPDATE objects SET locked_until=$1, metadata=$2 WHERE id=$3`
	if s.dialect == dialectPostgres {
		query = `UPDATE objects SET locked_until=$1, metadata=$2::jsonb WHERE id=$3`
	}
	result, err := s.db.ExecContext(
		ctx, s.rebind(query),
		until.UTC().Format(time.RFC3339Nano), string(metaBytes), objectID,
	)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
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

// ListStorageKeys returns every distinct storage_key in the objects table,
// including soft-deleted rows (they still pin their blob).
func (s *sqlStore) ListStorageKeys(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT DISTINCT storage_key FROM objects WHERE storage_key <> ''`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
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

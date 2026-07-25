package repository

import (
	"context"
	"database/sql"
	"errors"
)

func (s *sqlStore) PutLegalHold(ctx context.Context, l LegalHold) error {
	l.TenantID = defaultTenant(l.TenantID)
	var q string
	if s.dialect == dialectPostgres {
		q = `INSERT INTO legal_holds (object_id, tenant_id, version_id, hold_reason, created_by, created_at)
VALUES ($1,$2,$3,$4,$5,now())
ON CONFLICT (object_id, version_id) DO UPDATE SET hold_reason=EXCLUDED.hold_reason, created_by=EXCLUDED.created_by`
	} else {
		q = `INSERT INTO legal_holds (object_id, tenant_id, version_id, hold_reason, created_by, created_at)
VALUES ($1,$2,$3,$4,$5,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
ON CONFLICT (object_id, version_id) DO UPDATE SET hold_reason=excluded.hold_reason, created_by=excluded.created_by`
	}
	vid := nullVersionID(l.VersionID)
	_, err := s.db.ExecContext(ctx, s.rebind(q), l.ObjectID, l.TenantID, vid, l.HoldReason, l.CreatedBy)
	return err
}

func (s *sqlStore) GetLegalHold(ctx context.Context, objectID int64, versionID string) (LegalHold, error) {
	var q string
	var args []any
	if versionID == "" {
		q = `SELECT object_id, tenant_id, COALESCE(version_id,''), hold_reason, created_by, created_at
FROM legal_holds WHERE object_id=$1 AND version_id IS NULL`
		args = []any{objectID}
	} else {
		q = `SELECT object_id, tenant_id, COALESCE(version_id,''), hold_reason, created_by, created_at
FROM legal_holds WHERE object_id=$1 AND version_id=$2`
		args = []any{objectID, versionID}
	}
	row := s.db.QueryRowContext(ctx, s.rebind(q), args...)
	var l LegalHold
	var vid sql.NullString
	if err := row.Scan(&l.ObjectID, &l.TenantID, &vid, &l.HoldReason, &l.CreatedBy, &l.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LegalHold{}, ErrNotFound
		}
		return LegalHold{}, err
	}
	if vid.Valid {
		l.VersionID = vid.String
	}
	return l, nil
}

func (s *sqlStore) ListLegalHolds(ctx context.Context, objectID int64) ([]LegalHold, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT object_id, tenant_id, COALESCE(version_id,''), hold_reason, created_by, created_at
FROM legal_holds WHERE object_id=$1 ORDER BY created_at DESC`), objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LegalHold
	for rows.Next() {
		var l LegalHold
		var vid sql.NullString
		if err := rows.Scan(&l.ObjectID, &l.TenantID, &vid, &l.HoldReason, &l.CreatedBy, &l.CreatedAt); err != nil {
			return nil, err
		}
		if vid.Valid {
			l.VersionID = vid.String
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *sqlStore) RemoveLegalHold(ctx context.Context, objectID int64, versionID string) error {
	var q string
	var args []any
	if versionID == "" {
		q = `DELETE FROM legal_holds WHERE object_id=$1 AND version_id IS NULL`
		args = []any{objectID}
	} else {
		q = `DELETE FROM legal_holds WHERE object_id=$1 AND version_id=$2`
		args = []any{objectID, versionID}
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

// ObjectHasLegalHold returns true if any legal hold exists for the given object
// (applying to either all versions or a specific version).
func (s *sqlStore) ObjectHasLegalHold(ctx context.Context, objectID int64) (bool, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, s.rebind(`SELECT COUNT(1) FROM legal_holds WHERE object_id=$1`), objectID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListObjectsOnLegalHold returns object IDs that have at least one active legal
// hold for the given tenant. Used by lifecycle/retention sweeps to skip objects
// that must not be deleted.
func (s *sqlStore) ListObjectsOnLegalHold(ctx context.Context, tenant string, limit int) ([]int64, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT DISTINCT object_id FROM legal_holds WHERE tenant_id=$1 LIMIT $2`), defaultTenant(tenant), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// nullVersionID returns a *string suitable for SQL NULL when versionID is
// empty (meaning the hold applies to all versions), or the string itself.
func nullVersionID(vid string) *string {
	if vid == "" {
		return nil
	}
	return &vid
}

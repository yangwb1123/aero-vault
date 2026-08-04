package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const tenantCols = `tenant_id, display_name, status, created_at`

// UpsertTenant inserts or updates a tenant keyed by tenant_id. On first insert
// created_at is set to now (unless the caller supplied one); on conflict the
// original created_at is preserved (it is excluded from the UPDATE SET) while
// display_name and status are refreshed.
func (s *sqlStore) UpsertTenant(ctx context.Context, tr TenantRecord) error {
	created := tr.CreatedAt
	if created == "" {
		created = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if tr.Status == "" {
		tr.Status = "active"
	}
	_, err := s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO tenants (tenant_id, display_name, status, created_at)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		   display_name = $5,
		   status       = $6`),
		tr.TenantID, tr.DisplayName, tr.Status, created,
		tr.DisplayName, tr.Status)
	return err
}

// GetTenant loads a tenant by id. The bool is false (with a nil error) when no
// row exists.
func (s *sqlStore) GetTenant(ctx context.Context, tenantID string) (TenantRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT `+tenantCols+` FROM tenants WHERE tenant_id=$1`), tenantID)
	var rec TenantRecord
	if err := row.Scan(&rec.TenantID, &rec.DisplayName, &rec.Status, &rec.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TenantRecord{}, false, nil
		}
		return TenantRecord{}, false, err
	}
	return rec, true, nil
}

// ListTenants returns every tenant ordered by created_at. The result is always
// non-nil.
func (s *sqlStore) ListTenants(ctx context.Context) ([]TenantRecord, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT `+tenantCols+` FROM tenants ORDER BY created_at`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]TenantRecord, 0)
	for rows.Next() {
		var rec TenantRecord
		if err := rows.Scan(&rec.TenantID, &rec.DisplayName, &rec.Status, &rec.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// DeleteTenant removes an empty tenant and its tenant-scoped control-plane
// records. Data-bearing resources must first be removed through FileService so
// storage blobs, chunks, locks, quotas, and events follow their normal lifecycle.
func (s *sqlStore) DeleteTenant(ctx context.Context, tenantID string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	query := `SELECT tenant_id FROM tenants WHERE tenant_id=$1`
	if s.dialect == dialectPostgres {
		query += ` FOR UPDATE`
	}
	var found string
	if err := tx.QueryRowContext(ctx, s.rebind(query), tenantID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	hasData, err := tenantHasStoredData(ctx, tx, s.rebind, tenantID)
	if err != nil {
		return false, err
	}
	if hasData {
		return false, ErrTenantNotEmpty
	}
	if err := deleteTenantControlPlane(ctx, tx, s.rebind, tenantID); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, s.rebind(
		`DELETE FROM tenants WHERE tenant_id=$1`), tenantID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	return true, tx.Commit()
}

func tenantHasStoredData(ctx context.Context, tx *sql.Tx, rebind func(string) string, tenant string) (bool, error) {
	row := tx.QueryRowContext(ctx, rebind(
		`SELECT 1 FROM (
		   SELECT tenant_id FROM objects WHERE tenant_id=$1
		   UNION ALL SELECT tenant_id FROM buckets WHERE tenant_id=$2
		   UNION ALL SELECT tenant_id FROM multipart_uploads WHERE tenant_id=$3
		 ) tenant_data LIMIT 1`), tenant, tenant, tenant)
	var one int
	err := row.Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func deleteTenantControlPlane(ctx context.Context, tx *sql.Tx, rebind func(string) string, tenant string) error {
	tables := []string{
		"legal_holds", "chunks", "ai_usage", "object_events", "jobs",
		"idempotency_keys", "api_keys", "tenant_quotas", "department_members",
		"resource_acls", "shares", "public_assets", "departments",
	}
	for _, table := range tables {
		if _, err := tx.ExecContext(ctx, rebind(`DELETE FROM `+table+` WHERE tenant_id=$1`), tenant); err != nil {
			return err
		}
	}
	return nil
}

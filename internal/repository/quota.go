package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// TenantQuota is the persistent record for a tenant's usage + caps.
type TenantQuota struct {
	TenantID          string
	MaxBytes          int64
	MaxObjects        int64
	UsedBytes         int64
	UsedObjects       int64
	DailyBudgetMicros int64 // per-tenant daily AI spend cap (USD-millionths); 0 = use the global default
	UpdatedAt         time.Time
}

const quotaCols = `tenant_id, max_bytes, max_objects, used_bytes, used_objects, daily_budget_micros, updated_at`

// ListTenantQuotas returns every tenant's quota/usage row — used to emit
// per-tenant storage gauges.
func (s *sqlStore) ListTenantQuotas(ctx context.Context) ([]TenantQuota, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+quotaCols+` FROM tenant_quotas`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TenantQuota{}
	for rows.Next() {
		var (
			q TenantQuota
			t flexTime
		)
		if err := rows.Scan(&q.TenantID, &q.MaxBytes, &q.MaxObjects, &q.UsedBytes, &q.UsedObjects, &q.DailyBudgetMicros, &t); err != nil {
			return nil, err
		}
		q.UpdatedAt = t.Time
		out = append(out, q)
	}
	return out, rows.Err()
}

// GetTenantQuota returns the row for a tenant (creating a zero row if absent).
func (s *sqlStore) GetTenantQuota(ctx context.Context, tenant string) (TenantQuota, error) {
	tenant = defaultTenant(tenant)
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT `+quotaCols+` FROM tenant_quotas WHERE tenant_id=$1`), tenant)
	var (
		q TenantQuota
		t flexTime
	)
	if err := row.Scan(&q.TenantID, &q.MaxBytes, &q.MaxObjects, &q.UsedBytes, &q.UsedObjects, &q.DailyBudgetMicros, &t); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TenantQuota{TenantID: tenant}, nil
		}
		return TenantQuota{}, err
	}
	q.UpdatedAt = t.Time
	return q, nil
}

// SetTenantQuota writes the cap fields without touching usage counters.
func (s *sqlStore) SetTenantQuota(ctx context.Context, tenant string, maxBytes, maxObjects int64) error {
	tenant = defaultTenant(tenant)
	var q string
	if s.dialect == dialectPostgres {
		q = `INSERT INTO tenant_quotas (tenant_id, max_bytes, max_objects) VALUES ($1,$2,$3)
ON CONFLICT (tenant_id) DO UPDATE SET max_bytes=EXCLUDED.max_bytes, max_objects=EXCLUDED.max_objects, updated_at=now()`
	} else {
		q = `INSERT INTO tenant_quotas (tenant_id, max_bytes, max_objects) VALUES ($1,$2,$3)
ON CONFLICT (tenant_id) DO UPDATE SET max_bytes=excluded.max_bytes, max_objects=excluded.max_objects, updated_at=excluded.updated_at`
	}
	_, err := s.db.ExecContext(ctx, s.rebind(q), tenant, maxBytes, maxObjects)
	return err
}

// SetTenantBudgetMicros writes a tenant's daily AI spend cap (USD-millionths)
// without touching its storage caps or usage counters. 0 clears the override so
// the global default applies again.
func (s *sqlStore) SetTenantBudgetMicros(ctx context.Context, tenant string, micros int64) error {
	tenant = defaultTenant(tenant)
	var q string
	if s.dialect == dialectPostgres {
		q = `INSERT INTO tenant_quotas (tenant_id, daily_budget_micros) VALUES ($1,$2)
ON CONFLICT (tenant_id) DO UPDATE SET daily_budget_micros=EXCLUDED.daily_budget_micros, updated_at=now()`
	} else {
		q = `INSERT INTO tenant_quotas (tenant_id, daily_budget_micros) VALUES ($1,$2)
ON CONFLICT (tenant_id) DO UPDATE SET daily_budget_micros=excluded.daily_budget_micros, updated_at=excluded.updated_at`
	}
	_, err := s.db.ExecContext(ctx, s.rebind(q), tenant, micros)
	return err
}

// AddTenantUsage atomically increments usage counters and returns the new
// values. delta values may be negative for deletions.
func (s *sqlStore) AddTenantUsage(ctx context.Context, tenant string, deltaBytes, deltaObjects int64) (TenantQuota, error) {
	tenant = defaultTenant(tenant)
	// upsert and increment in one go via a transaction for portability.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TenantQuota{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT OR IGNORE INTO tenant_quotas (tenant_id) VALUES ($1)`), tenant); err != nil {
		// Postgres syntax differs.
		if s.dialect == dialectPostgres {
			if _, e2 := tx.ExecContext(ctx, `INSERT INTO tenant_quotas (tenant_id) VALUES ($1) ON CONFLICT (tenant_id) DO NOTHING`, tenant); e2 != nil {
				return TenantQuota{}, e2
			}
		} else {
			return TenantQuota{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE tenant_quotas SET used_bytes = used_bytes + $1, used_objects = used_objects + $2, updated_at = $3 WHERE tenant_id = $4`),
		deltaBytes, deltaObjects, time.Now().UTC().Format(time.RFC3339Nano), tenant); err != nil {
		return TenantQuota{}, err
	}
	if err := tx.Commit(); err != nil {
		return TenantQuota{}, err
	}
	return s.GetTenantQuota(ctx, tenant)
}

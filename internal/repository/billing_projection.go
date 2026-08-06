package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

const billingProjectionCols = `tenant_id, revision, active, bytes_hard, bytes_unlimited, objects_hard, objects_unlimited, effective_at_ns, expires_at_ns, projected_at_ns`

func (s *sqlStore) GetBillingProjection(
	ctx context.Context, tenant string,
) (BillingProjection, bool, error) {
	tenant = defaultTenant(tenant)
	row := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT `+billingProjectionCols+` FROM billing_entitlements WHERE tenant_id=$1`), tenant)
	projection, err := scanBillingProjection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return BillingProjection{}, false, nil
	}
	return projection, err == nil, err
}

func (s *sqlStore) ApplyBillingProjection(
	ctx context.Context, projection BillingProjection,
) (bool, error) {
	if err := validateBillingProjection(projection); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	accepted, err := s.upsertBillingProjection(ctx, tx, projection)
	if err != nil || !accepted {
		if err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	if err := s.projectBillingQuota(ctx, tx, projection); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func validateBillingProjection(projection BillingProjection) error {
	if projection.TenantID == "" || projection.Revision == 0 || projection.Revision > math.MaxInt64 {
		return errors.New("billing projection identity is invalid")
	}
	if projection.Bytes.Hard < 0 || projection.Objects.Hard < 0 || projection.EffectiveAt.IsZero() {
		return errors.New("billing projection limits or effective time are invalid")
	}
	if (projection.Bytes.Unlimited && projection.Bytes.Hard != 0) ||
		(projection.Objects.Unlimited && projection.Objects.Hard != 0) {
		return errors.New("unlimited billing projection cannot carry a hard limit")
	}
	if projection.ProjectedAt.IsZero() {
		return errors.New("billing projection timestamp is required")
	}
	return nil
}

func (s *sqlStore) upsertBillingProjection(
	ctx context.Context, tx *sql.Tx, p BillingProjection,
) (bool, error) {
	q := `INSERT INTO billing_entitlements (` + billingProjectionCols + `)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (tenant_id) DO UPDATE SET
revision=excluded.revision, active=excluded.active, bytes_hard=excluded.bytes_hard,
bytes_unlimited=excluded.bytes_unlimited, objects_hard=excluded.objects_hard,
objects_unlimited=excluded.objects_unlimited, effective_at_ns=excluded.effective_at_ns,
expires_at_ns=excluded.expires_at_ns, projected_at_ns=excluded.projected_at_ns
WHERE billing_entitlements.revision < excluded.revision`
	result, err := tx.ExecContext(ctx, s.rebind(q),
		p.TenantID, int64(p.Revision), billingBoolInt(p.Active), p.Bytes.Hard,
		billingBoolInt(p.Bytes.Unlimited), p.Objects.Hard, billingBoolInt(p.Objects.Unlimited),
		p.EffectiveAt.UnixNano(), unixNanoOrZero(p.ExpiresAt), p.ProjectedAt.UnixNano())
	if err != nil {
		return false, fmt.Errorf("upsert billing projection: %w", err)
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *sqlStore) projectBillingQuota(
	ctx context.Context, tx *sql.Tx, p BillingProjection,
) error {
	insert := `INSERT OR IGNORE INTO tenant_quotas (tenant_id) VALUES ($1)`
	if s.dialect == dialectPostgres {
		insert = `INSERT INTO tenant_quotas (tenant_id) VALUES ($1) ON CONFLICT (tenant_id) DO NOTHING`
	}
	if _, err := tx.ExecContext(ctx, s.rebind(insert), p.TenantID); err != nil {
		return fmt.Errorf("ensure projected tenant quota: %w", err)
	}
	_, err := tx.ExecContext(ctx, s.rebind(`UPDATE tenant_quotas
SET max_bytes=$1, max_objects=$2, updated_at=$3 WHERE tenant_id=$4`),
		localQuotaLimit(p.Bytes), localQuotaLimit(p.Objects),
		p.ProjectedAt.UTC().Format(time.RFC3339Nano), p.TenantID)
	if err != nil {
		return fmt.Errorf("apply projected tenant quota: %w", err)
	}
	return nil
}

func scanBillingProjection(row rowScanner) (BillingProjection, error) {
	var (
		p                                        BillingProjection
		revision                                 int64
		active, bytesUnlimited, objectsUnlimited int
		effective, expires, projected            int64
	)
	err := row.Scan(&p.TenantID, &revision, &active, &p.Bytes.Hard, &bytesUnlimited,
		&p.Objects.Hard, &objectsUnlimited, &effective, &expires, &projected)
	if err != nil {
		return BillingProjection{}, err
	}
	p.Revision = uint64(revision)
	p.Active = active != 0
	p.Bytes.Unlimited = bytesUnlimited != 0
	p.Objects.Unlimited = objectsUnlimited != 0
	p.EffectiveAt = time.Unix(0, effective).UTC()
	p.ExpiresAt = timeFromUnixNano(expires)
	p.ProjectedAt = time.Unix(0, projected).UTC()
	return p, nil
}

func localQuotaLimit(limit BillingLimit) int64 {
	if limit.Unlimited {
		return 0
	}
	return limit.Hard
}

func billingBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func unixNanoOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

func timeFromUnixNano(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value).UTC()
}

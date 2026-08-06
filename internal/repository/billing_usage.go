package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

const billingUsageCols = `id, operation_id, tenant_id, dimension, quantity, occurred_at_ns, metadata_json, attempts, claim_owner`

func (s *sqlStore) ApplyBillingUsage(
	ctx context.Context, mutation BillingUsageMutation,
) (TenantQuota, bool, error) {
	if err := validateBillingMutation(mutation); err != nil {
		return TenantQuota{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TenantQuota{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	inserted, err := s.insertBillingOperation(ctx, tx, mutation)
	if err != nil {
		return TenantQuota{}, false, err
	}
	if !inserted {
		return s.finishDuplicateBillingUsage(ctx, tx, mutation)
	}
	quota, err := s.updateBillingUsage(ctx, tx, mutation)
	if err != nil {
		return TenantQuota{}, false, err
	}
	if err := s.insertBillingFacts(ctx, tx, mutation); err != nil {
		return TenantQuota{}, false, err
	}
	return quota, false, tx.Commit()
}

func validateBillingMutation(m BillingUsageMutation) error {
	if m.OperationID == "" || m.TenantID == "" || m.Kind == "" || len(m.Kind) > 64 || m.OccurredAt.IsZero() {
		return errors.New("billing usage mutation identity is invalid")
	}
	if m.DeltaBytes == math.MinInt64 || m.DeltaObjects == math.MinInt64 {
		return errors.New("billing usage mutation delta is invalid")
	}
	return nil
}

func (s *sqlStore) insertBillingOperation(
	ctx context.Context, tx *sql.Tx, m BillingUsageMutation,
) (bool, error) {
	now := time.Now().UTC().UnixNano()
	result, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO billing_usage_operations
(operation_id, tenant_id, delta_bytes, delta_objects, kind, occurred_at_ns, created_at_ns)
VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (operation_id) DO NOTHING`),
		m.OperationID, m.TenantID, m.DeltaBytes, m.DeltaObjects, m.Kind,
		m.OccurredAt.UnixNano(), now)
	if err != nil {
		return false, fmt.Errorf("insert billing usage operation: %w", err)
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func (s *sqlStore) finishDuplicateBillingUsage(
	ctx context.Context, tx *sql.Tx, m BillingUsageMutation,
) (TenantQuota, bool, error) {
	var tenant, kind string
	var bytes, objects int64
	err := tx.QueryRowContext(ctx, s.rebind(`SELECT tenant_id, delta_bytes, delta_objects, kind
FROM billing_usage_operations WHERE operation_id=$1`), m.OperationID).
		Scan(&tenant, &bytes, &objects, &kind)
	if err != nil {
		return TenantQuota{}, false, err
	}
	if tenant != m.TenantID || bytes != m.DeltaBytes || objects != m.DeltaObjects || kind != m.Kind {
		return TenantQuota{}, false, errors.New("billing usage operation id conflict")
	}
	quota, err := s.getTenantQuotaTx(ctx, tx, m.TenantID)
	if err != nil {
		return TenantQuota{}, false, err
	}
	return quota, true, tx.Commit()
}

func (s *sqlStore) updateBillingUsage(
	ctx context.Context, tx *sql.Tx, m BillingUsageMutation,
) (TenantQuota, error) {
	insert := `INSERT OR IGNORE INTO tenant_quotas (tenant_id) VALUES ($1)`
	if s.dialect == dialectPostgres {
		insert = `INSERT INTO tenant_quotas (tenant_id) VALUES ($1) ON CONFLICT (tenant_id) DO NOTHING`
	}
	if _, err := tx.ExecContext(ctx, s.rebind(insert), m.TenantID); err != nil {
		return TenantQuota{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := tx.ExecContext(ctx, s.rebind(`UPDATE tenant_quotas
SET used_bytes=CASE WHEN used_bytes+$1 < 0 THEN 0 ELSE used_bytes+$2 END,
    used_objects=CASE WHEN used_objects+$3 < 0 THEN 0 ELSE used_objects+$4 END,
    updated_at=$5 WHERE tenant_id=$6`),
		m.DeltaBytes, m.DeltaBytes, m.DeltaObjects, m.DeltaObjects, now, m.TenantID)
	if err != nil {
		return TenantQuota{}, err
	}
	return s.getTenantQuotaTx(ctx, tx, m.TenantID)
}

func (s *sqlStore) getTenantQuotaTx(ctx context.Context, tx *sql.Tx, tenant string) (TenantQuota, error) {
	row := tx.QueryRowContext(ctx, s.rebind(
		`SELECT `+quotaCols+` FROM tenant_quotas WHERE tenant_id=$1`), tenant)
	var q TenantQuota
	var updated flexTime
	if err := row.Scan(&q.TenantID, &q.MaxBytes, &q.MaxObjects, &q.UsedBytes,
		&q.UsedObjects, &q.DailyBudgetMicros, &updated); err != nil {
		return TenantQuota{}, err
	}
	q.UpdatedAt = updated.Time
	return q, nil
}

func (s *sqlStore) insertBillingFacts(
	ctx context.Context, tx *sql.Tx, m BillingUsageMutation,
) error {
	metadata, err := json.Marshal(map[string]string{"operation": m.Kind})
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixNano()
	for _, fact := range factsForMutation(m) {
		_, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO billing_usage_outbox
(id, operation_id, tenant_id, dimension, quantity, occurred_at_ns, metadata_json, next_attempt_at_ns, created_at_ns)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`), fact.ID, m.OperationID, m.TenantID,
			fact.Dimension, fact.Quantity, m.OccurredAt.UnixNano(), string(metadata), now, now)
		if err != nil {
			return fmt.Errorf("insert billing usage fact: %w", err)
		}
	}
	return nil
}

func factsForMutation(m BillingUsageMutation) []BillingUsageFact {
	facts := make([]BillingUsageFact, 0, 2)
	if m.DeltaBytes > 0 {
		facts = append(facts, newBillingFact(m, "bytes-allocated", BillingDimensionBytesAllocated, m.DeltaBytes))
	} else if m.DeltaBytes < 0 {
		facts = append(facts, newBillingFact(m, "bytes-reclaimed", BillingDimensionBytesReclaimed, -m.DeltaBytes))
	}
	if m.DeltaObjects > 0 {
		facts = append(facts, newBillingFact(m, "objects-created", BillingDimensionObjectsCreated, m.DeltaObjects))
	} else if m.DeltaObjects < 0 {
		facts = append(facts, newBillingFact(m, "objects-deleted", BillingDimensionObjectsDeleted, -m.DeltaObjects))
	}
	return facts
}

func newBillingFact(m BillingUsageMutation, suffix, dimension string, quantity int64) BillingUsageFact {
	return BillingUsageFact{
		ID: m.OperationID + "." + suffix, OperationID: m.OperationID,
		TenantID: m.TenantID, Dimension: dimension, Quantity: quantity,
		OccurredAt: m.OccurredAt,
	}
}

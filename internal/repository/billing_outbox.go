package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (s *sqlStore) ClaimBillingUsage(
	ctx context.Context, owner string, limit int, ttl time.Duration,
) ([]BillingUsageFact, error) {
	if owner == "" || ttl <= 0 {
		return nil, fmt.Errorf("billing usage claim identity is invalid")
	}
	if limit <= 0 || limit > 500 {
		limit = 32
	}
	if s.dialect == dialectPostgres {
		return s.claimBillingUsagePostgres(ctx, owner, limit, ttl)
	}
	return s.claimBillingUsageSQLite(ctx, owner, limit, ttl)
}

func (s *sqlStore) claimBillingUsagePostgres(
	ctx context.Context, owner string, limit int, ttl time.Duration,
) ([]BillingUsageFact, error) {
	now := time.Now().UTC().UnixNano()
	until := time.Now().UTC().Add(ttl).UnixNano()
	q := `UPDATE billing_usage_outbox SET status='inflight', attempts=attempts+1,
claim_owner=$1, claim_until_ns=$2
WHERE id IN (SELECT id FROM billing_usage_outbox
 WHERE (status='pending' AND next_attempt_at_ns <= $3)
    OR (status='inflight' AND claim_until_ns <= $4)
 ORDER BY created_at_ns, id LIMIT $5 FOR UPDATE SKIP LOCKED)
RETURNING ` + billingUsageCols
	rows, err := s.db.QueryContext(ctx, q, owner, until, now, now, limit)
	if err != nil {
		return nil, err
	}
	return scanBillingUsageRows(rows)
}

func (s *sqlStore) claimBillingUsageSQLite(
	ctx context.Context, owner string, limit int, ttl time.Duration,
) ([]BillingUsageFact, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().UnixNano()
	rows, err := tx.QueryContext(ctx, s.rebind(`SELECT id FROM billing_usage_outbox
WHERE (status='pending' AND next_attempt_at_ns <= $1)
   OR (status='inflight' AND claim_until_ns <= $2)
ORDER BY created_at_ns, id LIMIT $3`), now, now, limit)
	if err != nil {
		return nil, err
	}
	ids, err := scanStringRows(rows)
	if err != nil {
		return nil, err
	}
	facts, err := s.claimBillingUsageIDs(ctx, tx, owner, ids, now, ttl)
	if err != nil {
		return nil, err
	}
	return facts, tx.Commit()
}

func (s *sqlStore) claimBillingUsageIDs(
	ctx context.Context, tx *sql.Tx, owner string, ids []string, now int64, ttl time.Duration,
) ([]BillingUsageFact, error) {
	facts := make([]BillingUsageFact, 0, len(ids))
	until := time.Unix(0, now).UTC().Add(ttl).UnixNano()
	for _, id := range ids {
		row := tx.QueryRowContext(ctx, s.rebind(`UPDATE billing_usage_outbox
SET status='inflight', attempts=attempts+1, claim_owner=$1, claim_until_ns=$2
WHERE id=$3 AND ((status='pending' AND next_attempt_at_ns <= $4)
 OR (status='inflight' AND claim_until_ns <= $5)) RETURNING `+billingUsageCols),
			owner, until, id, now, now)
		fact, err := scanBillingUsageRow(row)
		if err == nil {
			facts = append(facts, fact)
		} else if err != sql.ErrNoRows {
			return nil, err
		}
	}
	return facts, nil
}

func scanStringRows(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func scanBillingUsageRows(rows *sql.Rows) ([]BillingUsageFact, error) {
	defer rows.Close()
	var facts []BillingUsageFact
	for rows.Next() {
		fact, err := scanBillingUsageRow(rows)
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

func scanBillingUsageRow(row rowScanner) (BillingUsageFact, error) {
	var fact BillingUsageFact
	var occurred int64
	err := row.Scan(&fact.ID, &fact.OperationID, &fact.TenantID, &fact.Dimension,
		&fact.Quantity, &occurred, &fact.MetadataJSON, &fact.Attempts, &fact.ClaimOwner)
	fact.OccurredAt = timeFromUnixNano(occurred)
	return fact, err
}

func (s *sqlStore) CompleteBillingUsage(ctx context.Context, id, owner string) error {
	now := time.Now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE billing_usage_outbox
SET status='delivered', delivered_at_ns=$1, claim_owner='', claim_until_ns=0, last_error=''
WHERE id=$2 AND status='inflight' AND claim_owner=$3`), now, id, owner)
	return requireBillingClaim(result, err)
}

func (s *sqlStore) RetryBillingUsage(
	ctx context.Context, id, owner, lastErr string, next time.Time,
) error {
	if len(lastErr) > 512 {
		lastErr = lastErr[:512]
	}
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE billing_usage_outbox
SET status='pending', next_attempt_at_ns=$1, claim_owner='', claim_until_ns=0, last_error=$2
WHERE id=$3 AND status='inflight' AND claim_owner=$4`),
		next.UTC().UnixNano(), strings.TrimSpace(lastErr), id, owner)
	return requireBillingClaim(result, err)
}

func requireBillingClaim(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errorsNewBillingClaimLost()
	}
	return nil
}

func errorsNewBillingClaimLost() error {
	return fmt.Errorf("billing usage claim lost")
}

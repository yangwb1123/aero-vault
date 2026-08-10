package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type deliveredGovernanceOrigin struct {
	id          string
	originKind  string
	originID    int64
	deliveredAt int64
}

func (s *sqlStore) CleanupDeliveredAuditGovernance(
	ctx context.Context, before time.Time, limit int,
) (int64, error) {
	if before.IsZero() || limit <= 0 || limit > 500 {
		return 0, errors.New("audit governance cleanup arguments are invalid")
	}
	if s.dialect == dialectPostgres {
		return s.cleanupDeliveredGovernancePostgres(ctx, before, limit)
	}
	return s.cleanupDeliveredGovernanceSQLite(ctx, before, limit)
}

func (s *sqlStore) cleanupDeliveredGovernancePostgres(
	ctx context.Context, before time.Time, limit int,
) (int64, error) {
	result, err := s.db.ExecContext(ctx, `WITH selected AS (
 SELECT id,origin_kind,origin_id,delivered_at_ns FROM audit_governance_outbox
 WHERE delivered_at_ns>0 AND delivered_at_ns <= $1
 ORDER BY delivered_at_ns,id LIMIT $2 FOR UPDATE SKIP LOCKED
), marked AS (
 INSERT INTO audit_governance_delivered_origins (origin_kind,origin_id,delivered_at_ns)
 SELECT origin_kind,origin_id,delivered_at_ns FROM selected
 ON CONFLICT (origin_kind,origin_id) DO NOTHING
)
DELETE FROM audit_governance_outbox o USING selected s WHERE o.id=s.id`,
		before.UTC().UnixNano(), limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *sqlStore) cleanupDeliveredGovernanceSQLite(
	ctx context.Context, before time.Time, limit int,
) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	origins, err := s.selectDeliveredGovernance(ctx, tx, before, limit)
	if err != nil {
		return 0, err
	}
	for _, origin := range origins {
		if err := s.cleanupDeliveredGovernanceOrigin(ctx, tx, origin, before); err != nil {
			return 0, err
		}
	}
	return int64(len(origins)), tx.Commit()
}

func (s *sqlStore) selectDeliveredGovernance(
	ctx context.Context, tx *sql.Tx, before time.Time, limit int,
) ([]deliveredGovernanceOrigin, error) {
	rows, err := tx.QueryContext(ctx, s.rebind(`SELECT id,origin_kind,origin_id,delivered_at_ns
FROM audit_governance_outbox WHERE delivered_at_ns>0 AND delivered_at_ns <= $1
ORDER BY delivered_at_ns,id LIMIT $2`), before.UTC().UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var origins []deliveredGovernanceOrigin
	for rows.Next() {
		var origin deliveredGovernanceOrigin
		if err := rows.Scan(&origin.id, &origin.originKind, &origin.originID,
			&origin.deliveredAt); err != nil {
			return nil, err
		}
		origins = append(origins, origin)
	}
	return origins, rows.Err()
}

func (s *sqlStore) cleanupDeliveredGovernanceOrigin(
	ctx context.Context, tx *sql.Tx, origin deliveredGovernanceOrigin, before time.Time,
) error {
	_, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO audit_governance_delivered_origins
(origin_kind,origin_id,delivered_at_ns) VALUES ($1,$2,$3)
ON CONFLICT (origin_kind,origin_id) DO NOTHING`),
		origin.originKind, origin.originID, origin.deliveredAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.rebind(`DELETE FROM audit_governance_outbox
WHERE id=$1 AND delivered_at_ns>0 AND delivered_at_ns <= $2`),
		origin.id, before.UTC().UnixNano())
	return err
}

// CleanupFailedAuditGovernance prunes terminal-failed rows older than the
// retention window (terminal-with-retention). Unlike delivered cleanup there
// is no origin tombstone: a failed row's origin was never ledgered, so a
// later mutation of the same origin may enqueue a fresh fact (no dedupe
// collision). DELETE-style, no gap-scan — the 7d default retention window is
// the documented delivery-recovery SLA.
func (s *sqlStore) CleanupFailedAuditGovernance(
	ctx context.Context, before time.Time, limit int,
) (int64, error) {
	if before.IsZero() || limit <= 0 || limit > 500 {
		return 0, errors.New("audit governance failed cleanup arguments are invalid")
	}
	if s.dialect == dialectPostgres {
		result, err := s.db.ExecContext(ctx, `DELETE FROM audit_governance_outbox
WHERE id IN (
 SELECT id FROM audit_governance_outbox
 WHERE failed_at_ns>0 AND failed_at_ns <= $1
 ORDER BY failed_at_ns,id LIMIT $2 FOR UPDATE SKIP LOCKED)`,
			before.UTC().UnixNano(), limit)
		if err != nil {
			return 0, err
		}
		return result.RowsAffected()
	}
	result, err := s.db.ExecContext(ctx, s.rebind(`DELETE FROM audit_governance_outbox
WHERE id IN (
 SELECT id FROM audit_governance_outbox
 WHERE failed_at_ns>0 AND failed_at_ns <= $1
 ORDER BY failed_at_ns,id LIMIT $2)`),
		before.UTC().UnixNano(), limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

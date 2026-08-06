package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const auditGovernanceCols = `id,tenant_id,origin_kind,origin_id,fact_kind,actor_digest,
action,target_digest,request_id,detail_sha256,object_size_bytes,storage_backend,
occurred_at_ns,attempts,claim_owner,claim_token,lease_expires_at_ns`

func (s *sqlStore) ClaimAuditGovernance(
	ctx context.Context, owner, token string, revision uint64, limit int, ttl time.Duration,
) ([]AuditGovernanceFact, error) {
	if owner == "" || token == "" || revision == 0 || ttl <= 0 {
		return nil, errors.New("audit governance claim identity is invalid")
	}
	if limit <= 0 || limit > 500 {
		limit = 32
	}
	if s.dialect == dialectPostgres {
		return s.claimAuditGovernancePostgres(ctx, owner, token, revision, limit, ttl)
	}
	return s.claimAuditGovernanceSQLite(ctx, owner, token, revision, limit, ttl)
}

func (s *sqlStore) claimAuditGovernancePostgres(
	ctx context.Context, owner, token string, revision uint64, limit int, ttl time.Duration,
) ([]AuditGovernanceFact, error) {
	now := time.Now().UTC()
	query := `UPDATE audit_governance_outbox SET attempts=attempts+1,claim_owner=$1,
claim_token=$2,lease_expires_at_ns=$3 WHERE id IN (
 SELECT o.id FROM audit_governance_outbox o JOIN audit_governance_bindings b
 ON b.tenant_id=o.tenant_id WHERE o.delivered_at_ns=0
 AND o.available_at_ns <= $4 AND o.lease_expires_at_ns <= $5
 AND b.revision=$6
 ORDER BY o.available_at_ns,o.created_at_ns,o.id LIMIT $7 FOR UPDATE OF o SKIP LOCKED)
RETURNING ` + auditGovernanceCols
	rows, err := s.db.QueryContext(ctx, query, owner, token, now.Add(ttl).UnixNano(),
		now.UnixNano(), now.UnixNano(), revision, limit)
	if err != nil {
		return nil, err
	}
	return scanAuditGovernanceRows(rows)
}

func (s *sqlStore) claimAuditGovernanceSQLite(
	ctx context.Context, owner, token string, revision uint64, limit int, ttl time.Duration,
) ([]AuditGovernanceFact, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	rows, err := tx.QueryContext(ctx, s.rebind(`SELECT o.id FROM audit_governance_outbox o
JOIN audit_governance_bindings b ON b.tenant_id=o.tenant_id
WHERE o.delivered_at_ns=0 AND o.available_at_ns <= $1 AND o.lease_expires_at_ns <= $2
AND b.revision=$3
ORDER BY o.available_at_ns,o.created_at_ns,o.id LIMIT $4`),
		now.UnixNano(), now.UnixNano(), revision, limit)
	if err != nil {
		return nil, err
	}
	ids, err := scanStringRows(rows)
	if err != nil {
		return nil, err
	}
	facts, err := s.claimAuditGovernanceIDs(ctx, tx, owner, token, ids, now, ttl)
	if err != nil {
		return nil, err
	}
	return facts, tx.Commit()
}

func (s *sqlStore) claimAuditGovernanceIDs(
	ctx context.Context, tx *sql.Tx, owner, token string, ids []string, now time.Time, ttl time.Duration,
) ([]AuditGovernanceFact, error) {
	facts := make([]AuditGovernanceFact, 0, len(ids))
	for _, id := range ids {
		row := tx.QueryRowContext(ctx, s.rebind(`UPDATE audit_governance_outbox
SET attempts=attempts+1,claim_owner=$1,claim_token=$2,lease_expires_at_ns=$3
WHERE id=$4 AND delivered_at_ns=0 AND available_at_ns <= $5 AND lease_expires_at_ns <= $6
RETURNING `+auditGovernanceCols), owner, token, now.Add(ttl).UnixNano(), id,
			now.UnixNano(), now.UnixNano())
		fact, err := scanAuditGovernanceRow(row)
		if err == nil {
			facts = append(facts, fact)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return facts, nil
}

func scanAuditGovernanceRows(rows *sql.Rows) ([]AuditGovernanceFact, error) {
	defer rows.Close()
	facts := make([]AuditGovernanceFact, 0)
	for rows.Next() {
		fact, err := scanAuditGovernanceRow(rows)
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

func scanAuditGovernanceRow(row rowScanner) (AuditGovernanceFact, error) {
	var fact AuditGovernanceFact
	var occurred, lease int64
	err := row.Scan(&fact.ID, &fact.TenantID, &fact.OriginKind, &fact.OriginID,
		&fact.FactKind, &fact.ActorDigest, &fact.Action, &fact.TargetDigest,
		&fact.RequestID, &fact.DetailSHA256, &fact.ObjectSizeBytes, &fact.StorageBackend,
		&occurred, &fact.Attempts, &fact.ClaimOwner, &fact.ClaimToken, &lease)
	fact.OccurredAt, fact.LeaseExpiresAt = timeFromUnixNano(occurred), timeFromUnixNano(lease)
	return fact, err
}

func (s *sqlStore) CompleteAuditGovernance(
	ctx context.Context, id, owner, token string,
) error {
	now := time.Now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE audit_governance_outbox
SET delivered_at_ns=$1,claim_owner='',claim_token='',lease_expires_at_ns=0,last_error=''
WHERE id=$2 AND delivered_at_ns=0 AND claim_owner=$3 AND claim_token=$4
AND lease_expires_at_ns > $5`), now, id, owner, token, now)
	return requireGovernanceClaim(result, err)
}

func (s *sqlStore) RetryAuditGovernance(
	ctx context.Context, id, owner, token, lastErr string, next time.Time,
) error {
	if len(lastErr) > 512 {
		lastErr = lastErr[:512]
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE audit_governance_outbox
SET available_at_ns=$1,claim_owner='',claim_token='',lease_expires_at_ns=0,last_error=$2
WHERE id=$3 AND delivered_at_ns=0 AND claim_owner=$4 AND claim_token=$5
AND lease_expires_at_ns > $6`), next.UTC().UnixNano(), strings.TrimSpace(lastErr),
		id, owner, token, now)
	return requireGovernanceClaim(result, err)
}

func requireGovernanceClaim(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("audit governance claim lost")
	}
	return nil
}

func (s *sqlStore) OldestPendingAuditGovernance(
	ctx context.Context,
) (time.Time, bool, error) {
	var value sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MIN(o.created_at_ns)
FROM audit_governance_outbox o JOIN audit_governance_bindings b
 ON b.tenant_id=o.tenant_id WHERE o.delivered_at_ns=0`).Scan(&value)
	if err != nil || !value.Valid {
		return time.Time{}, false, err
	}
	return timeFromUnixNano(value.Int64), true, nil
}

func (s *sqlStore) HasPendingDrainingAuditGovernance(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
 SELECT 1 FROM audit_governance_outbox o JOIN audit_governance_bindings b
  ON b.tenant_id=o.tenant_id
 WHERE o.delivered_at_ns=0 AND b.state='draining')`).Scan(&exists)
	return exists, err
}

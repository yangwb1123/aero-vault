package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// OutboxEventType is the versioned event-fact namespace of the deletion
// transactional outbox. Both facts of one delete are written in the same
// transaction as the delete itself (FR-1/FR-2).
type OutboxEventType string

const (
	// EventTypeFileDeleted11 is the lifecycle fact: a durable, self-contained
	// record of the delete. The relay completes it without re-broadcasting to
	// local subscribers (D3) — it exists for forward compatibility.
	EventTypeFileDeleted11 OutboxEventType = "vault.file.deleted@1.1"
	// EventTypeFileNotify11 is the S3-notification-shaped fact delivered by the
	// relay to configured bucket-notification targets (payload carried as-is).
	EventTypeFileNotify11 OutboxEventType = "vault.file.notify@1.1"
)

// maxOutboxFactPayloadBytes bounds one fact payload at insert time (F9/G4):
// an oversized payload is a programming error and must roll the delete
// transaction back with the rest of the validation, not blow up the L2
// target at delivery time (the POST-side check is defense in depth only).
const maxOutboxFactPayloadBytes = 1 << 20 // 1 MiB

// OutboxFact is one versioned event fact written atomically with the delete.
// OriginID is the objects.id of the deleted row (informational only; the
// authority dedupe key is the outbox row id — D1).
type OutboxFact struct {
	EventType OutboxEventType
	OriginID  int64
	TenantID  string
	Payload   []byte // self-contained JSON (FR-2), stored byte-exact
}

// EventOutboxRow is one claimed fact returned by ClaimEventOutbox.
type EventOutboxRow struct {
	ID             int64
	EventType      OutboxEventType
	OriginID       int64
	TenantID       string
	Payload        []byte
	Attempts       int
	ClaimOwner     string
	ClaimToken     string
	LeaseExpiresAt time.Time
}

var errEventOutboxClaimLost = errors.New("event outbox claim lost")

// ── Fact validation (inside the delete transaction; failure rolls back) ──────

func validateOutboxFacts(facts []OutboxFact) error {
	if len(facts) == 0 {
		return errors.New("event outbox facts must not be empty")
	}
	for _, fact := range facts {
		if fact.EventType != EventTypeFileDeleted11 && fact.EventType != EventTypeFileNotify11 {
			return fmt.Errorf("event outbox fact type %q is invalid", fact.EventType)
		}
		if fact.OriginID <= 0 {
			return errors.New("event outbox fact origin is invalid")
		}
		if strings.TrimSpace(fact.TenantID) == "" {
			return errors.New("event outbox fact tenant is required")
		}
		if len(fact.Payload) == 0 || len(fact.Payload) > maxOutboxFactPayloadBytes {
			return errors.New("event outbox fact payload must be within 1..1048576 bytes")
		}
		if !validOutboxPayload(fact.Payload) {
			return errors.New("event outbox fact payload must be JSON with schema_version 1.1")
		}
	}
	return nil
}

func validOutboxPayload(payload []byte) bool {
	var doc map[string]any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return false
	}
	version, _ := doc["schema_version"].(string)
	return version == "1.1"
}

// ── Atomic delete + facts (FR-1) ─────────────────────────────────────────────

// HardDeleteObjectWithEvent performs the full HardDeleteObject transaction
// (legal-hold check, access-state cleanup, DELETE FROM objects), inserts the
// audit_log row (FR-1: L0 is always-on) and every outbox fact in the same
// transaction. A zero-row delete (concurrent double-delete race) rolls back
// and returns ErrNotFound so no phantom facts or audit rows are left behind
// (GAP-4 — stricter than the plain HardDeleteObject).
func (s *sqlStore) HardDeleteObjectWithEvent(
	ctx context.Context, tenant, bucket, key string, entry AuditEntry, facts []OutboxFact,
) error {
	tenant = defaultTenant(tenant)
	if err := validateOutboxFacts(facts); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var held bool
	if err := tx.QueryRowContext(ctx, s.rebind(`
SELECT EXISTS (SELECT 1 FROM objects o
  JOIN legal_holds h ON h.object_id = o.id
  WHERE o.tenant_id=$1 AND o.bucket=$2 AND o.key=$3)`), tenant, bucket, key).Scan(&held); err != nil {
		return err
	}
	if held {
		return ErrLegalHoldActive
	}
	if err := deleteObjectAccessState(ctx, s, tx, tenant, bucket, key); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM objects WHERE tenant_id=$1 AND bucket=$2 AND key=$3`), tenant, bucket, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := insertAuditEntry(ctx, s, tx, entry); err != nil {
		return err
	}
	if err := insertOutboxFacts(ctx, s, tx, facts); err != nil {
		return err
	}
	return tx.Commit()
}

// SoftDeleteObjectWithEvent performs the SoftDeleteObject transaction
// (UPDATE deleted_at, ErrNotFound on zero rows, access-state cleanup) and
// inserts the audit_log row (FR-1) plus every outbox fact in the same
// transaction.
func (s *sqlStore) SoftDeleteObjectWithEvent(
	ctx context.Context, tenant, bucket, key string, entry AuditEntry, facts []OutboxFact,
) error {
	tenant = defaultTenant(tenant)
	if err := validateOutboxFacts(facts); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, s.rebind(`UPDATE objects SET deleted_at=$1 WHERE tenant_id=$2 AND bucket=$3 AND key=$4 AND deleted_at IS NULL`),
		time.Now().UTC().Format(time.RFC3339Nano), tenant, bucket, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if err := deleteObjectAccessState(ctx, s, tx, tenant, bucket, key); err != nil {
		return err
	}
	if err := insertAuditEntry(ctx, s, tx, entry); err != nil {
		return err
	}
	if err := insertOutboxFacts(ctx, s, tx, facts); err != nil {
		return err
	}
	return tx.Commit()
}

// SoftDeleteObjectByIDWithEvent performs the SoftDeleteObjectByID transaction
// (SELECT by id, UPDATE deleted_at, ErrNotFound on zero rows, access-state
// cleanup) and inserts the audit_log row (FR-1) plus every outbox fact in the
// same transaction. Unlike the keyed variants, validation runs inside the
// transaction (D-2): a malformed fact rolls the whole delete back — no soft
// delete, no audit row, no phantom outbox rows.
func (s *sqlStore) SoftDeleteObjectByIDWithEvent(
	ctx context.Context, id int64, entry AuditEntry, facts []OutboxFact,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateOutboxFacts(facts); err != nil {
		return err
	}
	var tenant, bucket, key string
	if err := tx.QueryRowContext(ctx, s.rebind(
		`SELECT tenant_id, bucket, key FROM objects WHERE id=$1 AND deleted_at IS NULL`,
	), id).Scan(&tenant, &bucket, &key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	res, err := tx.ExecContext(
		ctx,
		s.rebind(`UPDATE objects SET deleted_at=$1 WHERE id=$2 AND deleted_at IS NULL`),
		time.Now().UTC().Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := deleteObjectAccessState(ctx, s, tx, tenant, bucket, key); err != nil {
		return err
	}
	if err := insertAuditEntry(ctx, s, tx, entry); err != nil {
		return err
	}
	if err := insertOutboxFacts(ctx, s, tx, facts); err != nil {
		return err
	}
	return tx.Commit()
}

func insertOutboxFacts(ctx context.Context, store *sqlStore, exec governanceExecutor, facts []OutboxFact) error {
	now := time.Now().UTC().UnixNano()
	for _, fact := range facts {
		if _, err := exec.ExecContext(ctx, store.rebind(`INSERT INTO event_outbox
(event_type, origin_id, tenant_id, payload, available_at_ns, created_at_ns)
VALUES ($1,$2,$3,$4,$5,$6)`),
			string(fact.EventType), fact.OriginID, fact.TenantID, string(fact.Payload), now, now); err != nil {
			return err
		}
	}
	return nil
}

// ── Relay: claim → deliver → complete (FR-3, billing claim shape) ────────────

const eventOutboxCols = `id,event_type,origin_id,tenant_id,payload,
attempts,claim_owner,claim_token,lease_expires_at_ns`

// ClaimEventOutbox claims up to limit due facts for owner, fenced with a
// fresh token and a lease of ttl. Due = pending with available_at reached, or
// inflight whose lease expired (crash redelivery). Returns fewer rows when
// concurrent claimers win — callers must tolerate short batches.
func (s *sqlStore) ClaimEventOutbox(
	ctx context.Context, owner, token string, limit int, ttl time.Duration,
) ([]EventOutboxRow, error) {
	if owner == "" || token == "" || ttl <= 0 {
		return nil, errors.New("event outbox claim identity is invalid")
	}
	if limit <= 0 || limit > 500 {
		limit = 32
	}
	if s.dialect == dialectPostgres {
		return s.claimEventOutboxPostgres(ctx, owner, token, limit, ttl)
	}
	return s.claimEventOutboxSQLite(ctx, owner, token, limit, ttl)
}

func (s *sqlStore) claimEventOutboxPostgres(
	ctx context.Context, owner, token string, limit int, ttl time.Duration,
) ([]EventOutboxRow, error) {
	now := time.Now().UTC().UnixNano()
	until := time.Now().UTC().Add(ttl).UnixNano()
	query := `UPDATE event_outbox SET status='inflight', attempts=attempts+1,
claim_owner=$1, claim_token=$2, lease_expires_at_ns=$3
WHERE id IN (SELECT id FROM event_outbox
 WHERE (status='pending' AND available_at_ns <= $4)
    OR (status='inflight' AND lease_expires_at_ns <= $5)
 ORDER BY available_at_ns, created_at_ns, id LIMIT $6 FOR UPDATE SKIP LOCKED)
RETURNING ` + eventOutboxCols
	rows, err := s.db.QueryContext(ctx, query, owner, token, until, now, now, limit)
	if err != nil {
		return nil, err
	}
	return scanEventOutboxRows(rows)
}

func (s *sqlStore) claimEventOutboxSQLite(
	ctx context.Context, owner, token string, limit int, ttl time.Duration,
) ([]EventOutboxRow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().UnixNano()
	rows, err := tx.QueryContext(ctx, s.rebind(`SELECT id FROM event_outbox
WHERE (status='pending' AND available_at_ns <= $1)
   OR (status='inflight' AND lease_expires_at_ns <= $2)
ORDER BY available_at_ns, created_at_ns, id LIMIT $3`), now, now, limit)
	if err != nil {
		return nil, err
	}
	ids, err := scanInt64Rows(rows)
	if err != nil {
		return nil, err
	}
	facts, err := s.claimEventOutboxIDs(ctx, tx, owner, token, ids, now, ttl)
	if err != nil {
		return nil, err
	}
	return facts, tx.Commit()
}

func (s *sqlStore) claimEventOutboxIDs(
	ctx context.Context, tx *sql.Tx, owner, token string, ids []int64, now int64, ttl time.Duration,
) ([]EventOutboxRow, error) {
	facts := make([]EventOutboxRow, 0, len(ids))
	until := time.Unix(0, now).UTC().Add(ttl).UnixNano()
	for _, id := range ids {
		row := tx.QueryRowContext(ctx, s.rebind(`UPDATE event_outbox
SET status='inflight', attempts=attempts+1, claim_owner=$1, claim_token=$2, lease_expires_at_ns=$3
WHERE id=$4 AND ((status='pending' AND available_at_ns <= $5)
 OR (status='inflight' AND lease_expires_at_ns <= $6)) RETURNING `+eventOutboxCols),
			owner, token, until, id, now, now)
		fact, err := scanEventOutboxRow(row)
		if err == nil {
			facts = append(facts, fact)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return facts, nil
}

// CompleteEventOutbox marks a claimed fact delivered. The status transition
// and the fidelity record are written in one transaction; the fencing guard
// (owner+token+live lease) makes a stale complete fail with ErrClaimLost.
func (s *sqlStore) CompleteEventOutbox(ctx context.Context, id int64, owner, token string) error {
	now := time.Now().UTC().UnixNano()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, s.rebind(`UPDATE event_outbox
SET status='delivered', delivered_at_ns=$1, claim_owner='', claim_token='', lease_expires_at_ns=0, last_error=''
WHERE id=$2 AND status='inflight' AND claim_owner=$3 AND claim_token=$4
  AND lease_expires_at_ns > $5`), now, id, owner, token, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errEventOutboxClaimLost
	}
	if _, err := tx.ExecContext(ctx, s.rebind(
		`INSERT INTO event_outbox_delivered (outbox_id, delivered_at_ns) VALUES ($1,$2)`), id, now); err != nil {
		return err
	}
	return tx.Commit()
}

// RetryEventOutbox reschedules a claimed fact after a delivery failure.
// attempts was already incremented at claim; when it reaches maxAttempts the
// fact becomes terminal 'failed' (never claimable again), otherwise it returns
// to 'pending' with the backoff time. Fencing failures return ErrClaimLost.
func (s *sqlStore) RetryEventOutbox(
	ctx context.Context, id int64, owner, token, lastErr string, next time.Time, maxAttempts int,
) error {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	if len(lastErr) > 512 {
		lastErr = lastErr[:512]
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE event_outbox
SET status = CASE WHEN attempts >= $1 THEN 'failed' ELSE 'pending' END,
    available_at_ns=$2, claim_owner='', claim_token='', lease_expires_at_ns=0, last_error=$3
WHERE id=$4 AND status='inflight' AND claim_owner=$5 AND claim_token=$6
  AND lease_expires_at_ns > $7`),
		maxAttempts, next.UTC().UnixNano(), strings.TrimSpace(lastErr), id, owner, token, now)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errEventOutboxClaimLost
	}
	return nil
}

// PruneEventOutbox removes delivered rows older than deliveredBefore and
// terminal-failed rows older than failedBefore. DELETE-style: no tombstones,
// nothing ever re-enqueues from this table (unlike audit governance, there is
// no gap-scan). Returns the total number of removed outbox rows.
func (s *sqlStore) PruneEventOutbox(ctx context.Context, deliveredBefore, failedBefore time.Time) (int64, error) {
	if deliveredBefore.IsZero() || failedBefore.IsZero() {
		return 0, errors.New("event outbox prune times are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM event_outbox_delivered WHERE outbox_id IN
 (SELECT id FROM event_outbox WHERE status='delivered' AND delivered_at_ns < $1)`),
		deliveredBefore.UTC().UnixNano()); err != nil {
		return 0, err
	}
	var removed int64
	for _, prune := range []struct {
		status string
		before time.Time
	}{
		{"delivered", deliveredBefore},
		{"failed", failedBefore},
	} {
		var res sql.Result
		var err error
		if prune.status == "delivered" {
			res, err = tx.ExecContext(ctx, s.rebind(`DELETE FROM event_outbox
WHERE status='delivered' AND delivered_at_ns < $1`), prune.before.UTC().UnixNano())
		} else {
			res, err = tx.ExecContext(ctx, s.rebind(`DELETE FROM event_outbox
WHERE status='failed' AND created_at_ns < $1`), prune.before.UTC().UnixNano())
		}
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	return removed, tx.Commit()
}

// HasEventOutboxFact reports whether any fact of the given type exists for the
// origin object, regardless of status. Used by the Notifier (D2): when the
// WithEvent transaction committed before s.emit, the row is visible, so the
// bus-level EventDeleted broadcast can be skipped for the relay-owned path.
func (s *sqlStore) HasEventOutboxFact(ctx context.Context, originID int64, eventType OutboxEventType) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
 SELECT 1 FROM event_outbox WHERE origin_id=$1 AND event_type=$2)`,
		originID, string(eventType)).Scan(&exists)
	return exists, err
}

// CountEventOutbox returns the total event_outbox row count (any status).
// Used only by the relay startup log (D6): while the relay is disabled this
// is the only outbox depth signal that exists. Dialect-neutral by
// construction — zero placeholders, so no rebind is needed (I1 unaffected).
func (s *sqlStore) CountEventOutbox(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM event_outbox`).Scan(&n)
	return n, err
}

func scanEventOutboxRows(rows *sql.Rows) ([]EventOutboxRow, error) {
	defer rows.Close()
	facts := make([]EventOutboxRow, 0)
	for rows.Next() {
		fact, err := scanEventOutboxRow(rows)
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

func scanEventOutboxRow(row rowScanner) (EventOutboxRow, error) {
	var fact EventOutboxRow
	var lease int64
	var eventType string
	err := row.Scan(&fact.ID, &eventType, &fact.OriginID, &fact.TenantID,
		&fact.Payload, &fact.Attempts, &fact.ClaimOwner, &fact.ClaimToken, &lease)
	fact.EventType = OutboxEventType(eventType)
	fact.LeaseExpiresAt = timeFromUnixNano(lease)
	return fact, err
}

func scanInt64Rows(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()
	var values []int64
	for rows.Next() {
		var value int64
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

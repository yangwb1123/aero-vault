package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Idempotency statuses.
const (
	IdempotencyInProgress = "in_progress"
	IdempotencyCompleted  = "completed"
)

const idempotencyCols = `tenant_id, idem_key, fingerprint, status, response_status, response_body, response_ct, request_id, created_at, completed_at`

// ClaimIdempotencyKey attempts to reserve (tenant, key) for a new write by
// inserting an 'in_progress' row. When the insert wins (no prior key) the fresh
// record is returned with claimed=true. When the key already exists the stored
// record is loaded and returned with claimed=false so the caller can replay or
// reject the duplicate.
func (s *sqlStore) ClaimIdempotencyKey(ctx context.Context, tenant, key, fingerprint, requestID string) (rec IdempotencyRecord, claimed bool, err error) {
	tenant = defaultTenant(tenant)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// ON CONFLICT DO NOTHING is the same syntax on both dialects here.
	res, err := s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO idempotency_keys (tenant_id, idem_key, fingerprint, status, request_id, created_at)
		 VALUES ($1,$2,$3,'in_progress',$4,$5)
		 ON CONFLICT (tenant_id, idem_key) DO NOTHING`),
		tenant, key, fingerprint, requestID, now)
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return IdempotencyRecord{
			TenantID:    tenant,
			Key:         key,
			Fingerprint: fingerprint,
			Status:      IdempotencyInProgress,
			RequestID:   requestID,
			CreatedAt:   now,
		}, true, nil
	}

	rec, err = s.getIdempotencyKey(ctx, tenant, key)
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	return rec, false, nil
}

// CompleteIdempotencyKey records the original request's response so retries can
// replay it, transitioning the row to 'completed'.
func (s *sqlStore) CompleteIdempotencyKey(ctx context.Context, tenant, key string, status int, body []byte, contentType string) error {
	tenant = defaultTenant(tenant)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE idempotency_keys
		 SET status='completed', response_status=$1, response_body=$2, response_ct=$3, completed_at=$4
		 WHERE tenant_id=$5 AND idem_key=$6`),
		status, body, contentType, now, tenant, key)
	return err
}

// DeleteIdempotencyKey releases a claim (e.g. the original request failed with a
// 5xx) so a future retry can claim the key afresh.
func (s *sqlStore) DeleteIdempotencyKey(ctx context.Context, tenant, key string) error {
	tenant = defaultTenant(tenant)
	_, err := s.db.ExecContext(ctx, s.rebind(
		`DELETE FROM idempotency_keys WHERE tenant_id=$1 AND idem_key=$2`), tenant, key)
	return err
}

// DeleteIdempotencyKeysBefore purges idempotency keys created before the given
// RFC3339 timestamp (TTL GC), returning how many rows were removed.
func (s *sqlStore) DeleteIdempotencyKeysBefore(ctx context.Context, before string) (int64, error) {
	res, err := s.db.ExecContext(ctx, s.rebind(
		`DELETE FROM idempotency_keys WHERE created_at < $1`), before)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// getIdempotencyKey loads the stored record for (tenant, key). Callers that have
// just lost the insert race always expect a row; ErrNoRows is surfaced as-is.
func (s *sqlStore) getIdempotencyKey(ctx context.Context, tenant, key string) (IdempotencyRecord, error) {
	row := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT `+idempotencyCols+` FROM idempotency_keys WHERE tenant_id=$1 AND idem_key=$2`), tenant, key)
	var (
		rec  IdempotencyRecord
		body []byte
	)
	if err := row.Scan(&rec.TenantID, &rec.Key, &rec.Fingerprint, &rec.Status, &rec.ResponseStatus, &body, &rec.ResponseCT, &rec.RequestID, &rec.CreatedAt, &rec.CompletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IdempotencyRecord{}, ErrNotFound
		}
		return IdempotencyRecord{}, err
	}
	rec.ResponseBody = body
	return rec, nil
}

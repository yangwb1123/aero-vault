package repository

import (
	"context"
	"time"
)

type WebhookFailure struct {
	ID           int64
	EventID      int64
	URL          string
	Payload      string
	Attempts     int
	LastError    string
	LastStatus   int
	NextRetryAt  time.Time
	Succeeded    bool
	DeadLettered bool
	CreatedAt    time.Time
}

// RecordWebhookFailure stores a failed delivery so a retry worker can pick it
// up.
func (s *sqlStore) RecordWebhookFailure(ctx context.Context, f WebhookFailure) (int64, error) {
	var q string
	if s.dialect == dialectPostgres {
		q = `INSERT INTO webhook_failures (event_id, url, payload, attempts, last_error, last_status, next_retry_at)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`
	} else {
		q = `INSERT INTO webhook_failures (event_id, url, payload, attempts, last_error, last_status, next_retry_at)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`
	}
	row := s.db.QueryRowContext(ctx, s.rebind(q),
		f.EventID, f.URL, f.Payload, f.Attempts, f.LastError, f.LastStatus,
		f.NextRetryAt.UTC().Format(time.RFC3339Nano))
	var id int64
	if err := row.Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// NextPendingFailures returns rows whose next_retry_at <= now and that are
// still flagged as not succeeded.
func (s *sqlStore) NextPendingFailures(ctx context.Context, limit int) ([]WebhookFailure, error) {
	if limit <= 0 {
		limit = 32
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var q string
	if s.dialect == dialectPostgres {
		// Postgres uses booleans for the succeeded flag.
		q = `
SELECT id, event_id, url, payload, attempts, last_error, last_status, next_retry_at,
       CASE WHEN succeeded THEN 1 ELSE 0 END,
       CASE WHEN dead_lettered THEN 1 ELSE 0 END, created_at
FROM webhook_failures WHERE succeeded = false AND dead_lettered = false AND next_retry_at <= $1 ORDER BY id LIMIT $2`
	} else {
		q = `
SELECT id, event_id, url, payload, attempts, last_error, last_status, next_retry_at,
       CASE WHEN succeeded THEN 1 ELSE 0 END,
       CASE WHEN dead_lettered THEN 1 ELSE 0 END, created_at
FROM webhook_failures WHERE succeeded = 0 AND dead_lettered = 0 AND next_retry_at <= $1 ORDER BY id LIMIT $2`
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(q), now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebhookFailure
	for rows.Next() {
		var (
			f       WebhookFailure
			nextT   flexTime
			created flexTime
			succ    int
			dead    int
		)
		if err := rows.Scan(&f.ID, &f.EventID, &f.URL, &f.Payload, &f.Attempts, &f.LastError, &f.LastStatus, &nextT, &succ, &dead, &created); err != nil {
			return nil, err
		}
		f.NextRetryAt = nextT.Time
		f.CreatedAt = created.Time
		f.Succeeded = succ != 0
		f.DeadLettered = dead != 0
		out = append(out, f)
	}
	return out, nil
}

func (s *sqlStore) MarkWebhookSucceeded(ctx context.Context, id int64) error {
	value := "1"
	if s.dialect == dialectPostgres {
		value = "true"
	}
	_, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE webhook_failures SET succeeded=`+value+` WHERE id=$1`,
	), id)
	return err
}

func (s *sqlStore) MarkWebhookDeadLettered(
	ctx context.Context, id int64, lastErr string, lastStatus, attempts int,
) error {
	value := "1"
	if s.dialect == dialectPostgres {
		value = "true"
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`
UPDATE webhook_failures
SET dead_lettered=`+value+`, last_error=$1, last_status=$2,
    next_retry_at=$3, attempts=$4
WHERE id=$5`),
		lastErr, lastStatus, time.Now().UTC().Format(time.RFC3339Nano), attempts, id)
	return err
}

func (s *sqlStore) UpdateWebhookFailure(ctx context.Context, id int64, lastErr string, lastStatus int, nextRetryAt time.Time, attempts int) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE webhook_failures SET last_error=$1, last_status=$2, next_retry_at=$3, attempts=$4 WHERE id=$5`),
		lastErr, lastStatus, nextRetryAt.UTC().Format(time.RFC3339Nano), attempts, id)
	return err
}

func (s *sqlStore) ListWebhookFailures(ctx context.Context, limit int) ([]WebhookFailure, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`
SELECT id, event_id, url, payload, attempts, last_error, last_status, next_retry_at,
       CASE WHEN succeeded THEN 1 ELSE 0 END,
       CASE WHEN dead_lettered THEN 1 ELSE 0 END, created_at
FROM webhook_failures ORDER BY id DESC LIMIT $1`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebhookFailure
	for rows.Next() {
		var (
			f       WebhookFailure
			nextT   flexTime
			created flexTime
			succ    int
			dead    int
		)
		if err := rows.Scan(&f.ID, &f.EventID, &f.URL, &f.Payload, &f.Attempts, &f.LastError, &f.LastStatus, &nextT, &succ, &dead, &created); err != nil {
			return nil, err
		}
		f.NextRetryAt = nextT.Time
		f.CreatedAt = created.Time
		f.Succeeded = succ != 0
		f.DeadLettered = dead != 0
		out = append(out, f)
	}
	return out, nil
}

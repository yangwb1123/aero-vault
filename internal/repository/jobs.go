package repository

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// Job statuses.
const (
	JobPending   = "pending"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
)

// Job is a unit of durable background work. Payload is opaque JSON interpreted
// by the handler registered for Type.
type Job struct {
	ID          int64
	TenantID    string
	Type        string
	Payload     string
	Status      string
	Priority    int
	Attempts    int
	MaxAttempts int
	RunAfter    time.Time
	LastError   string
	Worker      string
	Result      string
	DedupeKey   string // empty -> stored as NULL
	CreatedAt   time.Time
	UpdatedAt   time.Time
	StartedAt   time.Time // zero when not yet started
	FinishedAt  time.Time // zero when not finished
}

const jobCols = `id, tenant_id, type, payload, status, priority, attempts, max_attempts, run_after, last_error, worker, result, dedupe_key, created_at, updated_at, started_at, finished_at`

// EnqueueJob inserts a pending job. When DedupeKey is set, a live (pending or
// running) job with the same key short-circuits the insert and its id is
// returned with deduped=true — callers can safely fire-and-forget duplicates.
func (s *sqlStore) EnqueueJob(ctx context.Context, j Job) (id int64, deduped bool, err error) {
	j.TenantID = defaultTenant(j.TenantID)
	if j.MaxAttempts <= 0 {
		j.MaxAttempts = 5
	}
	if j.Payload == "" {
		j.Payload = "{}"
	}
	now := time.Now().UTC()
	if j.RunAfter.IsZero() {
		j.RunAfter = now
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()

	if j.DedupeKey != "" {
		row := tx.QueryRowContext(ctx, s.rebind(
			`SELECT id FROM jobs WHERE dedupe_key=$1 AND status IN ('pending','running') LIMIT 1`), j.DedupeKey)
		var existing int64
		switch e := row.Scan(&existing); {
		case e == nil:
			return existing, true, tx.Commit()
		case errors.Is(e, sql.ErrNoRows):
			// fall through to insert
		default:
			return 0, false, e
		}
	}

	var dedupe any
	if j.DedupeKey != "" {
		dedupe = j.DedupeKey
	}
	// NOTE: created_at/updated_at use distinct placeholders ($8,$9) bound to the
	// same value — reusing one placeholder breaks after rebind to SQLite's
	// anonymous '?', which is positional by count.
	row := tx.QueryRowContext(ctx, s.rebind(
		`INSERT INTO jobs (tenant_id, type, payload, status, priority, attempts, max_attempts, run_after, dedupe_key, created_at, updated_at)
		 VALUES ($1,$2,$3,'pending',$4,0,$5,$6,$7,$8,$9) RETURNING id`),
		j.TenantID, j.Type, j.Payload, j.Priority, j.MaxAttempts,
		j.RunAfter.Format(time.RFC3339Nano), dedupe, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err = row.Scan(&id); err != nil {
		return 0, false, err
	}
	return id, false, tx.Commit()
}

// ClaimJob atomically transitions the next runnable job to 'running',
// incrementing its attempt count, and returns it. ok=false means the queue is
// empty (no error). worker labels the claimer for observability.
func (s *sqlStore) ClaimJob(ctx context.Context, worker string) (Job, bool, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if s.dialect == dialectPostgres {
		q := `UPDATE jobs SET status='running', attempts=attempts+1, worker=$1, started_at=now(), updated_at=now()
		WHERE id = (
		  SELECT id FROM jobs WHERE status='pending' AND run_after <= now()
		  ORDER BY priority DESC, id ASC LIMIT 1 FOR UPDATE SKIP LOCKED
		) RETURNING ` + jobCols
		j, err := scanJobRow(s.db.QueryRowContext(ctx, q, worker))
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, false, nil
		}
		return j, err == nil, err
	}

	// SQLite: no SKIP LOCKED — claim inside a transaction with a guarded UPDATE.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	row := tx.QueryRowContext(ctx, s.rebind(
		`SELECT id FROM jobs WHERE status='pending' AND run_after <= $1 ORDER BY priority DESC, id ASC LIMIT 1`), now)
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, false, nil
		}
		return Job{}, false, err
	}
	res, err := tx.ExecContext(ctx, s.rebind(
		`UPDATE jobs SET status='running', attempts=attempts+1, worker=$1, started_at=$2, updated_at=$3 WHERE id=$4 AND status='pending'`),
		worker, now, now, id)
	if err != nil {
		return Job{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Job{}, false, nil // lost the race; caller retries
	}
	j, err := scanJobRow(tx.QueryRowContext(ctx, s.rebind(`SELECT `+jobCols+` FROM jobs WHERE id=$1`), id))
	if err != nil {
		return Job{}, false, err
	}
	return j, true, tx.Commit()
}

// CompleteJob marks a job succeeded with an optional result payload.
func (s *sqlStore) CompleteJob(ctx context.Context, id int64, result string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE jobs SET status='succeeded', result=$1, last_error='', finished_at=$2, updated_at=$3 WHERE id=$4`),
		result, now, now, id)
	return err
}

// RetryJob returns a job to the pending queue with a delayed run_after and the
// error that triggered the retry. Attempts were already bumped at claim time.
func (s *sqlStore) RetryJob(ctx context.Context, id int64, lastErr string, runAfter time.Time) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE jobs SET status='pending', last_error=$1, run_after=$2, worker='', updated_at=$3 WHERE id=$4`),
		lastErr, runAfter.UTC().Format(time.RFC3339Nano), now, id)
	return err
}

// FailJob marks a job permanently failed (max attempts exhausted).
func (s *sqlStore) FailJob(ctx context.Context, id int64, lastErr string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE jobs SET status='failed', last_error=$1, finished_at=$2, updated_at=$3 WHERE id=$4`),
		lastErr, now, now, id)
	return err
}

// CountJobsByStatus returns how many jobs currently have the given status
// (e.g. "pending") — used to enforce a queue-depth backpressure cap.
func (s *sqlStore) CountJobsByStatus(ctx context.Context, status string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, s.rebind(`SELECT COUNT(1) FROM jobs WHERE status = $1`), status).Scan(&n)
	return n, err
}

// ListJobs returns recent jobs, newest first, optionally filtered by status
// and/or type. Empty filters match all.
func (s *sqlStore) ListJobs(ctx context.Context, status, jobType string, limit int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + jobCols + ` FROM jobs WHERE 1=1`
	args := []any{}
	n := 1
	if status != "" {
		q += " AND status=$" + strconv.Itoa(n)
		args = append(args, status)
		n++
	}
	if jobType != "" {
		q += " AND type=$" + strconv.Itoa(n)
		args = append(args, jobType)
		n++
	}
	q += " ORDER BY id DESC LIMIT $" + strconv.Itoa(n)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, s.rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJobRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// JobStats counts jobs grouped by status (for the admin dashboard).
func (s *sqlStore) JobStats(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var st string
		var c int64
		if err := rows.Scan(&st, &c); err != nil {
			return nil, err
		}
		out[st] = c
	}
	return out, rows.Err()
}

// ReapStuckJobs returns jobs that have been 'running' longer than maxAge back to
// pending so a crashed worker's jobs get retried. Returns the count requeued.
func (s *sqlStore) ReapStuckJobs(ctx context.Context, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-maxAge).Format(time.RFC3339Nano)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, s.rebind(
		`UPDATE jobs SET status='pending', worker='', updated_at=$1 WHERE status='running' AND started_at <= $2`),
		now, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func scanJobRow(r *sql.Row) (Job, error)   { return scanJob(r) }
func scanJobRows(r *sql.Rows) (Job, error) { return scanJob(r) }

func scanJob(r rowScanner) (Job, error) {
	var (
		j        Job
		dedupe   sql.NullString
		runAfter flexTime
		created  flexTime
		updated  flexTime
		started  flexTime
		finished flexTime
	)
	if err := r.Scan(&j.ID, &j.TenantID, &j.Type, &j.Payload, &j.Status, &j.Priority,
		&j.Attempts, &j.MaxAttempts, &runAfter, &j.LastError, &j.Worker, &j.Result,
		&dedupe, &created, &updated, &started, &finished); err != nil {
		return Job{}, err
	}
	j.DedupeKey = dedupe.String
	j.RunAfter = runAfter.Time
	j.CreatedAt = created.Time
	j.UpdatedAt = updated.Time
	j.StartedAt = started.Time
	j.FinishedAt = finished.Time
	return j, nil
}

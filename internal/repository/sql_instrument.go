package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/aero-vault/aero-vault/internal/telemetry"
)

// newSQLStore wraps *sql.DB with query timing and returns a sqlStore.
func newSQLStore(rawDB *sql.DB, dialect dialect) *sqlStore {
	return &sqlStore{
		db:      newTimedDB(rawDB, "repo"),
		dialect: dialect,
	}
}

// timedDB wraps *sql.DB to inject query-latency instrumentation into every
// database call without changing every call site in the repository layer.
// The `op` label categorises queries for the sql.query_duration_ms metric.
//
// Because sql.DB has value methods (not interface), we embed and selectively
// override only the methods used by sqlStore. Unused methods pass through to
// the embedded *sql.DB.
type timedDB struct {
	*sql.DB
	op string // operation label (e.g. "repo")
}

func newTimedDB(db *sql.DB, op string) *timedDB {
	return &timedDB{DB: db, op: op}
}

func (t *timedDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	res, err := t.DB.ExecContext(ctx, query, args...)
	telemetry.RecordSQLQuery(ctx, t.op, float64(time.Since(start).Microseconds())/1000.0)
	return res, err
}

func (t *timedDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := t.DB.QueryContext(ctx, query, args...)
	telemetry.RecordSQLQuery(ctx, t.op, float64(time.Since(start).Microseconds())/1000.0)
	return rows, err
}

func (t *timedDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	start := time.Now()
	row := t.DB.QueryRowContext(ctx, query, args...)
	telemetry.RecordSQLQuery(ctx, t.op, float64(time.Since(start).Microseconds())/1000.0)
	return row
}

func (t *timedDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	start := time.Now()
	tx, err := t.DB.BeginTx(ctx, opts)
	telemetry.RecordSQLQuery(ctx, t.op+".tx", float64(time.Since(start).Microseconds())/1000.0)
	return tx, err
}

func (t *timedDB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	start := time.Now()
	stmt, err := t.DB.PrepareContext(ctx, query)
	telemetry.RecordSQLQuery(ctx, t.op+".prepare", float64(time.Since(start).Microseconds())/1000.0)
	return stmt, err
}

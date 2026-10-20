package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aero-vault/aero-vault/internal/spaces"
)

// Read snapshots keep the tenant check and the result in the same transaction.
// PG FOR SHARE needs a writable transaction, even for this read-only capability.
func spaceRead[T any](ctx context.Context, s *sqlStore, tenant string, read func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	var opts *sql.TxOptions
	if s.dialect == dialectPostgres {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead}
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return zero, spaceSQLError(ctx, "begin read", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.spaceTenant(ctx, tx, tenant); err != nil {
		return zero, spaceSQLError(ctx, "read tenant", err)
	}
	value, err := read(tx)
	if err != nil {
		return zero, spaceSQLError(ctx, "read", err)
	}
	if err := tx.Commit(); err != nil {
		return zero, spaceSQLError(ctx, "commit read", err)
	}
	return value, nil
}

func (s *sqlStore) spaceTenant(ctx context.Context, tx *sql.Tx, tenant string) error {
	q := `SELECT status FROM tenants WHERE tenant_id=$1`
	if s.dialect == dialectPostgres {
		q += ` FOR SHARE`
	}
	var status string
	err := tx.QueryRowContext(ctx, s.rebind(q), tenant).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return spaces.ErrTenantUnavailable
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return spaces.ErrTenantUnavailable
	}
	return nil
}

func (s *sqlStore) spaceRoot(ctx context.Context, tx *sql.Tx, tenant, id string) (spaces.Space, error) {
	return scanSpace(tx.QueryRowContext(ctx, s.rebind(`SELECT `+spaceColumns+` FROM spaces WHERE tenant_id=$1 AND id=$2`), tenant, id))
}

// The first statement is a write. SQLite thus never upgrades a stale read
// snapshot; PG locks the root row. No process mutex, count-before-CAS or retries.
func (s *sqlStore) spaceCAS(ctx context.Context, tx *sql.Tx, c spaces.MemberCommand, now time.Time) error {
	res, err := tx.ExecContext(ctx, s.rebind(`UPDATE spaces SET revision=revision+1,
updated_by_source=$1,updated_by_kind=$2,updated_by_id=$3,updated_at_ns=$4
WHERE tenant_id=$5 AND id=$6 AND revision=$7`),
		c.Actor.Source, c.Actor.Kind, c.Actor.ID, now.UnixNano(), c.TenantID, c.SpaceID, c.ExpectedRevision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if err := s.spaceTenant(ctx, tx, c.TenantID); err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	if n != 0 {
		return fmt.Errorf("unexpected Space CAS row count: %d", n)
	}
	if _, err := s.spaceRoot(ctx, tx, c.TenantID, c.SpaceID); err != nil {
		return err
	}
	return spaces.ErrConflict
}

func spaceOneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("unexpected Space write row count: %d", n)
	}
	return nil
}

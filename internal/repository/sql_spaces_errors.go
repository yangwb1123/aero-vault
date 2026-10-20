package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func spaceSQLError(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}
	if spaceDomainError(err) {
		return err
	}
	kind := spaces.ErrPersistence
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		kind = classifySpaceSQL(err)
	}
	return fmt.Errorf("spaces %s: %w: %w", op, kind, err)
}

func spaceDomainError(err error) bool {
	for _, kind := range []error{spaces.ErrInvalidArgument, spaces.ErrNotFound, spaces.ErrTenantUnavailable,
		spaces.ErrConflict, spaces.ErrMemberExists, spaces.ErrMemberRevoked, spaces.ErrLastAdmin,
		spaces.ErrConcurrent, spaces.ErrPersistence} {
		if errors.Is(err, kind) {
			return true
		}
	}
	return false
}

func classifySpaceSQL(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return spaces.ErrNotFound
	}
	var sq *sqlite.Error
	if errors.As(err, &sq) {
		return classifySpaceSQLite(sq)
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return classifySpacePostgres(pg)
	}
	return spaces.ErrPersistence
}

func classifySpaceSQLite(sq *sqlite.Error) error {
	switch sq.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return spaces.ErrConflict
	}
	if sq.Code()&0xff == sqlite3.SQLITE_BUSY || sq.Code()&0xff == sqlite3.SQLITE_LOCKED {
		return spaces.ErrConcurrent
	}
	return spaces.ErrPersistence
}

func classifySpacePostgres(pg *pgconn.PgError) error {
	switch pg.Code {
	case "40001", "40P01", "55P03":
		return spaces.ErrConcurrent
	case "23505":
		if spaceUniqueConstraint(pg.ConstraintName) {
			return spaces.ErrConflict
		}
	}
	return spaces.ErrPersistence
}

func spaceUniqueConstraint(name string) bool {
	switch name {
	case "buckets_pkey", "spaces_pkey", "spaces_bucket_uq", "spaces_personal_subject_uq",
		"space_members_pkey", "space_history_pkey", "space_history_command_uq", "space_history_revision_uq":
		return true
	}
	return false
}

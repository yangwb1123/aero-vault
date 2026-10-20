package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/aero-vault/aero-vault/internal/spaces"
)

func (s *sqlStore) CreateSpace(ctx context.Context, c spaces.CreateCommand) (spaces.Space, error) {
	if err := spaces.ValidateCreate(c); err != nil {
		return spaces.Space{}, err
	}
	now := time.Now().UTC()
	v := spaces.Space{TenantID: c.TenantID, ID: c.ID, Kind: c.Kind, Name: c.Name,
		BackingBucket: "spc-" + c.ID, Revision: 1, CreatedBy: c.Actor, UpdatedBy: c.Actor, CreatedAt: now, UpdatedAt: now}
	if c.Kind == spaces.Personal {
		actor := c.Actor
		v.PersonalSubject = &actor
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return spaces.Space{}, spaceSQLError(ctx, "begin create", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.spaceCreateRows(ctx, tx, v, c); err != nil {
		return spaces.Space{}, spaceSQLError(ctx, "create", err)
	}
	if err := tx.Commit(); err != nil {
		return spaces.Space{}, spaceSQLError(ctx, "commit create (outcome may be uncertain)", err)
	}
	return v, nil
}

func (s *sqlStore) spaceCreateRows(ctx context.Context, tx *sql.Tx, v spaces.Space, c spaces.CreateCommand) error {
	if s.dialect == dialectPostgres {
		if err := s.spaceTenant(ctx, tx, v.TenantID); err != nil {
			return err
		}
	}
	// Strict INSERT, not CreateBucket's ignore-on-conflict behavior. SQLite's
	// first access is this write, before checking the tenant in the same tx.
	var bucketTime any = v.CreatedAt
	if s.dialect == dialectSQLite {
		bucketTime = v.CreatedAt.Format(time.RFC3339Nano)
	}
	if err := spaceOneRow(tx.ExecContext(ctx, s.rebind(`INSERT INTO buckets (tenant_id,name,created_at) VALUES ($1,$2,$3)`),
		v.TenantID, v.BackingBucket, bucketTime)); err != nil {
		return err
	}
	if s.dialect == dialectSQLite {
		if err := s.spaceTenant(ctx, tx, v.TenantID); err != nil {
			return err
		}
	}
	if err := s.spaceInsertRoot(ctx, tx, v); err != nil {
		return err
	}
	m := spaces.Member{TenantID: v.TenantID, SpaceID: v.ID, Subject: c.Actor, Role: spaces.Admin, State: spaces.Active,
		ChangedRevision: 1, ChangedBy: c.Actor, JoinedAt: v.CreatedAt, UpdatedAt: v.CreatedAt}
	if err := s.spaceInsertMember(ctx, tx, m); err != nil {
		return err
	}
	return s.spaceAppendHistory(ctx, tx, spaces.History{TenantID: v.TenantID, SpaceID: v.ID, CommandID: c.CommandID,
		CommandType: "create", Actor: c.Actor, Target: c.Actor, BeforeRevision: 0, AfterRevision: 1,
		AfterRole: spaces.Admin, AfterState: spaces.Active, Reason: c.Reason, RequestID: c.RequestID, OccurredAt: v.CreatedAt})
}

func (s *sqlStore) spaceInsertRoot(ctx context.Context, tx *sql.Tx, v spaces.Space) error {
	var source, kind, id any
	if v.PersonalSubject != nil {
		source, kind, id = v.PersonalSubject.Source, v.PersonalSubject.Kind, v.PersonalSubject.ID
	}
	return spaceOneRow(tx.ExecContext(ctx, s.rebind(`INSERT INTO spaces (`+spaceColumns+`)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`),
		v.TenantID, v.ID, v.Kind, v.Name, v.BackingBucket, source, kind, id, v.Revision,
		v.CreatedBy.Source, v.CreatedBy.Kind, v.CreatedBy.ID, v.UpdatedBy.Source, v.UpdatedBy.Kind, v.UpdatedBy.ID,
		v.CreatedAt.UnixNano(), v.UpdatedAt.UnixNano()))
}

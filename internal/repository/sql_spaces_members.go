package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/aero-vault/aero-vault/internal/spaces"
)

func (s *sqlStore) GetSpaceMember(ctx context.Context, tenant, id string, subject spaces.SubjectRef) (spaces.Member, error) {
	if err := spaces.ValidateSpaceRef(tenant, id); err != nil {
		return spaces.Member{}, err
	}
	if err := spaces.ValidateSubject(subject); err != nil {
		return spaces.Member{}, err
	}
	return spaceRead(ctx, s, tenant, func(tx *sql.Tx) (spaces.Member, error) {
		if _, err := s.spaceRoot(ctx, tx, tenant, id); err != nil {
			return spaces.Member{}, err
		}
		return s.spaceMember(ctx, tx, tenant, id, subject)
	})
}

func (s *sqlStore) spaceMember(ctx context.Context, tx *sql.Tx, tenant, id string, subject spaces.SubjectRef) (spaces.Member, error) {
	return scanSpaceMember(tx.QueryRowContext(ctx, s.rebind(`SELECT `+spaceMemberColumns+` FROM space_members
WHERE tenant_id=$1 AND space_id=$2 AND subject_source=$3 AND subject_kind=$4 AND subject_id=$5`),
		tenant, id, subject.Source, subject.Kind, subject.ID))
}

func (s *sqlStore) ListSpaceMembers(ctx context.Context, tenant, id string, page spaces.MemberPage) ([]spaces.Member, error) {
	if err := spaces.ValidateSpaceRef(tenant, id); err != nil {
		return nil, err
	}
	if err := spaces.ValidateMemberPage(page); err != nil {
		return nil, err
	}
	return spaceRead(ctx, s, tenant, func(tx *sql.Tx) ([]spaces.Member, error) {
		if _, err := s.spaceRoot(ctx, tx, tenant, id); err != nil {
			return nil, err
		}
		q := `SELECT ` + spaceMemberColumns + ` FROM space_members WHERE tenant_id=$1 AND space_id=$2`
		args := []any{tenant, id}
		if page.After != nil {
			q += ` AND (subject_source,subject_kind,subject_id)>($3,$4,$5) ORDER BY subject_source,subject_kind,subject_id LIMIT $6`
			args = append(args, page.After.Source, page.After.Kind, page.After.ID, page.Limit)
		} else {
			q += ` ORDER BY subject_source,subject_kind,subject_id LIMIT $3`
			args = append(args, page.Limit)
		}
		rows, err := tx.QueryContext(ctx, s.rebind(q), args...)
		if err != nil {
			return nil, err
		}
		return collectSpaceRows(rows, scanSpaceMember)
	})
}

func (s *sqlStore) ChangeSpaceMember(ctx context.Context, c spaces.MemberCommand) (int64, error) {
	if err := spaces.ValidateMemberCommand(c); err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, spaceSQLError(ctx, "begin member", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	if err := s.spaceCAS(ctx, tx, c, now); err != nil {
		return 0, spaceSQLError(ctx, "member CAS", err)
	}
	if err := s.spaceChangeMemberRows(ctx, tx, c, now); err != nil {
		return 0, spaceSQLError(ctx, "member change", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, spaceSQLError(ctx, "commit member (outcome may be uncertain)", err)
	}
	return c.ExpectedRevision + 1, nil
}

func (s *sqlStore) spaceChangeMemberRows(ctx context.Context, tx *sql.Tx, c spaces.MemberCommand, now time.Time) error {
	before, err := s.spaceMember(ctx, tx, c.TenantID, c.SpaceID, c.Target)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	present := err == nil
	if err := spaceMemberState(c, before, present); err != nil {
		return err
	}
	after := spaceMemberAfter(c, before, now)
	if err := s.spaceProtectAdmin(ctx, tx, before, after); err != nil {
		return err
	}
	if c.Action == spaces.AddMember {
		if err := s.spaceInsertMember(ctx, tx, after); err != nil {
			return err
		}
	} else if err := s.spaceUpdateMember(ctx, tx, after); err != nil {
		return err
	}
	h := spaces.History{TenantID: c.TenantID, SpaceID: c.SpaceID, CommandID: c.CommandID, CommandType: string(c.Action),
		Actor: c.Actor, Target: c.Target, BeforeRevision: c.ExpectedRevision, AfterRevision: c.ExpectedRevision + 1,
		AfterRole: after.Role, AfterState: after.State, Reason: c.Reason, RequestID: c.RequestID, OccurredAt: now}
	if present {
		h.BeforeRole, h.BeforeState = before.Role, before.State
	}
	return s.spaceAppendHistory(ctx, tx, h)
}

func spaceMemberState(c spaces.MemberCommand, before spaces.Member, present bool) error {
	if c.Action == spaces.AddMember {
		if !present {
			return nil
		}
		if before.State == spaces.Revoked {
			return spaces.ErrMemberRevoked
		}
		return spaces.ErrMemberExists
	}
	if !present {
		return spaces.ErrNotFound
	}
	if c.Action == spaces.ReactivateMember {
		if before.State == spaces.Active {
			return spaces.ErrMemberExists
		}
		return nil
	}
	if before.State == spaces.Revoked {
		return spaces.ErrMemberRevoked
	}
	if c.Action == spaces.ChangeRole && before.Role == c.Role {
		return spaces.ErrConflict
	}
	return nil
}

func spaceMemberAfter(c spaces.MemberCommand, before spaces.Member, now time.Time) spaces.Member {
	after := before
	if c.Action == spaces.AddMember {
		after = spaces.Member{TenantID: c.TenantID, SpaceID: c.SpaceID, Subject: c.Target, JoinedAt: now}
	}
	after.State, after.Role, after.RevokedAt = spaces.Active, c.Role, nil
	if c.Action == spaces.RevokeMember {
		after.State, after.Role, after.RevokedAt = spaces.Revoked, before.Role, &now
	}
	after.ChangedRevision, after.ChangedBy, after.UpdatedAt = c.ExpectedRevision+1, c.Actor, now
	return after
}

func (s *sqlStore) spaceProtectAdmin(ctx context.Context, tx *sql.Tx, before, after spaces.Member) error {
	if before.Subject.Kind != spaces.User || before.State != spaces.Active || before.Role != spaces.Admin {
		return nil
	}
	if after.State == spaces.Active && after.Role == spaces.Admin {
		return nil
	}
	var count int64
	err := tx.QueryRowContext(ctx, s.rebind(`SELECT COUNT(*) FROM space_members
WHERE tenant_id=$1 AND space_id=$2 AND state='active' AND role='admin' AND subject_kind='user'`), before.TenantID, before.SpaceID).Scan(&count)
	if err != nil {
		return err
	}
	if count <= 1 {
		return spaces.ErrLastAdmin
	}
	return nil
}

func (s *sqlStore) spaceInsertMember(ctx context.Context, tx *sql.Tx, m spaces.Member) error {
	return spaceOneRow(tx.ExecContext(ctx, s.rebind(`INSERT INTO space_members (`+spaceMemberColumns+`)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`),
		m.TenantID, m.SpaceID, m.Subject.Source, m.Subject.Kind, m.Subject.ID, m.Role, m.State, m.ChangedRevision,
		m.ChangedBy.Source, m.ChangedBy.Kind, m.ChangedBy.ID, m.JoinedAt.UnixNano(), m.UpdatedAt.UnixNano(), spaceRevokedTime(m)))
}

func (s *sqlStore) spaceUpdateMember(ctx context.Context, tx *sql.Tx, m spaces.Member) error {
	return spaceOneRow(tx.ExecContext(ctx, s.rebind(`UPDATE space_members SET role=$1,state=$2,changed_revision=$3,
changed_by_source=$4,changed_by_kind=$5,changed_by_id=$6,updated_at_ns=$7,revoked_at_ns=$8
WHERE tenant_id=$9 AND space_id=$10 AND subject_source=$11 AND subject_kind=$12 AND subject_id=$13`),
		m.Role, m.State, m.ChangedRevision, m.ChangedBy.Source, m.ChangedBy.Kind, m.ChangedBy.ID, m.UpdatedAt.UnixNano(), spaceRevokedTime(m),
		m.TenantID, m.SpaceID, m.Subject.Source, m.Subject.Kind, m.Subject.ID))
}

func spaceRevokedTime(m spaces.Member) any {
	if m.RevokedAt != nil {
		return m.RevokedAt.UnixNano()
	}
	return nil
}

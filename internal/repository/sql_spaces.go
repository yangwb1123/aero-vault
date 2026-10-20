package repository

import (
	"database/sql"
	"time"

	"github.com/aero-vault/aero-vault/internal/spaces"
)

// Deliberately not embedded in Repository and not captured by production wiring.
var _ spaces.Store = (*sqlStore)(nil)

const spaceColumns = `tenant_id,id,kind,name,backing_bucket,personal_source,personal_kind,personal_subject_id,
revision,created_by_source,created_by_kind,created_by_id,updated_by_source,updated_by_kind,updated_by_id,created_at_ns,updated_at_ns`

const spaceMemberColumns = `tenant_id,space_id,subject_source,subject_kind,subject_id,role,state,changed_revision,
changed_by_source,changed_by_kind,changed_by_id,joined_at_ns,updated_at_ns,revoked_at_ns`

const spaceHistoryColumns = `tenant_id,id,space_id,command_id,command_type,actor_source,actor_kind,actor_id,
target_source,target_kind,target_id,before_revision,after_revision,before_role,after_role,before_state,after_state,reason,request_id,occurred_at_ns`

type spaceScanner interface{ Scan(...any) error }

func scanSpace(row spaceScanner) (spaces.Space, error) {
	var v spaces.Space
	var source, kind, id sql.NullString
	var created, updated int64
	err := row.Scan(&v.TenantID, &v.ID, &v.Kind, &v.Name, &v.BackingBucket, &source, &kind, &id,
		&v.Revision, &v.CreatedBy.Source, &v.CreatedBy.Kind, &v.CreatedBy.ID,
		&v.UpdatedBy.Source, &v.UpdatedBy.Kind, &v.UpdatedBy.ID, &created, &updated)
	if err != nil {
		return spaces.Space{}, err
	}
	if source.Valid {
		v.PersonalSubject = &spaces.SubjectRef{Source: source.String, Kind: spaces.SubjectKind(kind.String), ID: id.String}
	}
	v.CreatedAt, v.UpdatedAt = time.Unix(0, created).UTC(), time.Unix(0, updated).UTC()
	return v, nil
}

func scanSpaceMember(row spaceScanner) (spaces.Member, error) {
	var v spaces.Member
	var joined, updated int64
	var revoked sql.NullInt64
	err := row.Scan(&v.TenantID, &v.SpaceID, &v.Subject.Source, &v.Subject.Kind, &v.Subject.ID,
		&v.Role, &v.State, &v.ChangedRevision, &v.ChangedBy.Source, &v.ChangedBy.Kind, &v.ChangedBy.ID,
		&joined, &updated, &revoked)
	if err != nil {
		return spaces.Member{}, err
	}
	v.JoinedAt, v.UpdatedAt = time.Unix(0, joined).UTC(), time.Unix(0, updated).UTC()
	if revoked.Valid {
		t := time.Unix(0, revoked.Int64).UTC()
		v.RevokedAt = &t
	}
	return v, nil
}

func scanSpaceHistory(row spaceScanner) (spaces.History, error) {
	var v spaces.History
	var role, state sql.NullString
	var occurred int64
	err := row.Scan(&v.TenantID, &v.ID, &v.SpaceID, &v.CommandID, &v.CommandType,
		&v.Actor.Source, &v.Actor.Kind, &v.Actor.ID, &v.Target.Source, &v.Target.Kind, &v.Target.ID,
		&v.BeforeRevision, &v.AfterRevision, &role, &v.AfterRole, &state, &v.AfterState,
		&v.Reason, &v.RequestID, &occurred)
	if err != nil {
		return spaces.History{}, err
	}
	v.BeforeRole, v.BeforeState = spaces.Role(role.String), spaces.MemberState(state.String)
	v.OccurredAt = time.Unix(0, occurred).UTC()
	return v, nil
}

func collectSpaceRows[T any](rows *sql.Rows, scan func(spaceScanner) (T, error)) ([]T, error) {
	defer rows.Close()
	out := make([]T, 0)
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

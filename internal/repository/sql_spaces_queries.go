package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/spaces"
)

func (s *sqlStore) GetSpace(ctx context.Context, tenant, id string) (spaces.Space, error) {
	if err := spaces.ValidateSpaceRef(tenant, id); err != nil {
		return spaces.Space{}, err
	}
	return spaceRead(ctx, s, tenant, func(tx *sql.Tx) (spaces.Space, error) { return s.spaceRoot(ctx, tx, tenant, id) })
}

func (s *sqlStore) GetSpaceByBucket(ctx context.Context, tenant, bucket string) (spaces.Space, error) {
	if err := spaces.ValidateTenant(tenant); err != nil {
		return spaces.Space{}, err
	}
	if err := spaces.ValidateIdentifier(bucket); err != nil {
		return spaces.Space{}, err
	}
	return spaceRead(ctx, s, tenant, func(tx *sql.Tx) (spaces.Space, error) {
		return scanSpace(tx.QueryRowContext(ctx, s.rebind(`SELECT `+spaceColumns+` FROM spaces WHERE tenant_id=$1 AND backing_bucket=$2`), tenant, bucket))
	})
}

func (s *sqlStore) GetPersonalSpace(ctx context.Context, tenant string, subject spaces.SubjectRef) (spaces.Space, error) {
	if err := spaces.ValidateTenant(tenant); err != nil {
		return spaces.Space{}, err
	}
	if err := spaces.ValidateSubject(subject); err != nil {
		return spaces.Space{}, err
	}
	if subject.Kind != spaces.User {
		return spaces.Space{}, fmt.Errorf("%w: personal subject must be user", spaces.ErrInvalidArgument)
	}
	return spaceRead(ctx, s, tenant, func(tx *sql.Tx) (spaces.Space, error) {
		return scanSpace(tx.QueryRowContext(ctx, s.rebind(`SELECT `+spaceColumns+` FROM spaces
WHERE tenant_id=$1 AND kind='personal' AND personal_source=$2 AND personal_kind=$3 AND personal_subject_id=$4`),
			tenant, subject.Source, subject.Kind, subject.ID))
	})
}

func (s *sqlStore) ListSpacesForSubject(ctx context.Context, tenant string, subject spaces.SubjectRef, page spaces.SpacePage) ([]spaces.Space, error) {
	if err := spaces.ValidateTenant(tenant); err != nil {
		return nil, err
	}
	if err := spaces.ValidateSubject(subject); err != nil {
		return nil, err
	}
	if err := spaces.ValidateSpacePage(page); err != nil {
		return nil, err
	}
	return spaceRead(ctx, s, tenant, func(tx *sql.Tx) ([]spaces.Space, error) {
		// Tenant occurs twice, with TWO placeholders and TWO actual arguments (I1).
		rows, err := tx.QueryContext(ctx, s.rebind(`SELECT `+spaceColumns+` FROM spaces
WHERE tenant_id=$1 AND id IN (SELECT space_id FROM space_members
WHERE tenant_id=$2 AND subject_source=$3 AND subject_kind=$4 AND subject_id=$5 AND state='active')
AND id>$6 ORDER BY id LIMIT $7`), tenant, tenant, subject.Source, subject.Kind, subject.ID, page.AfterID, page.Limit)
		if err != nil {
			return nil, err
		}
		return collectSpaceRows(rows, scanSpace)
	})
}

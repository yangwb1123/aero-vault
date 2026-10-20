package repository

import (
	"context"
	"database/sql"

	"github.com/aero-vault/aero-vault/internal/spaces"
	"github.com/google/uuid"
)

// No standalone append capability: only the create/member transactions call it.
func (s *sqlStore) spaceAppendHistory(ctx context.Context, tx *sql.Tx, h spaces.History) error {
	id, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	var beforeRole, beforeState any
	if h.BeforeRole != "" {
		beforeRole = h.BeforeRole
	}
	if h.BeforeState != "" {
		beforeState = h.BeforeState
	}
	return spaceOneRow(tx.ExecContext(ctx, s.rebind(`INSERT INTO space_history (`+spaceHistoryColumns+`)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`),
		h.TenantID, id.String(), h.SpaceID, h.CommandID, h.CommandType, h.Actor.Source, h.Actor.Kind, h.Actor.ID,
		h.Target.Source, h.Target.Kind, h.Target.ID, h.BeforeRevision, h.AfterRevision, beforeRole, h.AfterRole,
		beforeState, h.AfterState, h.Reason, h.RequestID, h.OccurredAt.UnixNano()))
}

func (s *sqlStore) ListSpaceHistory(ctx context.Context, tenant, id string, page spaces.HistoryPage) ([]spaces.History, error) {
	if err := spaces.ValidateSpaceRef(tenant, id); err != nil {
		return nil, err
	}
	if err := spaces.ValidateHistoryPage(page); err != nil {
		return nil, err
	}
	return spaceRead(ctx, s, tenant, func(tx *sql.Tx) ([]spaces.History, error) {
		if _, err := s.spaceRoot(ctx, tx, tenant, id); err != nil {
			return nil, err
		}
		rows, err := tx.QueryContext(ctx, s.rebind(`SELECT `+spaceHistoryColumns+` FROM space_history
WHERE tenant_id=$1 AND space_id=$2 AND after_revision>$3 ORDER BY after_revision LIMIT $4`), tenant, id, page.AfterRevision, page.Limit)
		if err != nil {
			return nil, err
		}
		return collectSpaceRows(rows, scanSpaceHistory)
	})
}

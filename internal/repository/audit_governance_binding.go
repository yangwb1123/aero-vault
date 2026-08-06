package repository

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"
)

var (
	ErrAuditGovernanceRevisionRollback = errors.New("audit governance binding revision rollback")
	ErrAuditGovernanceRevisionDrift    = errors.New("audit governance binding revision drift")
)

const maxAuditGovernanceRevision = uint64(1<<63 - 1)

func (s *sqlStore) ApplyAuditGovernanceBindings(
	ctx context.Context, revision uint64, digest string, desired []AuditGovernanceBindingState,
) error {
	if revision == 0 || revision > maxAuditGovernanceRevision || digest == "" ||
		!validGovernanceBindingStates(desired) {
		return errors.New("audit governance desired binding state is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockGovernanceControl(ctx, tx); err != nil {
		return err
	}
	currentRevision, currentDigest, err := governanceControl(ctx, tx)
	if err != nil {
		return err
	}
	if revision < currentRevision {
		return ErrAuditGovernanceRevisionRollback
	}
	if revision == currentRevision && digest != currentDigest {
		return ErrAuditGovernanceRevisionDrift
	}
	if err := s.replaceGovernanceBindings(ctx, tx, revision, desired); err != nil {
		return err
	}
	unbound, err := s.unboundGovernanceBacklog(ctx, tx)
	if err != nil {
		return err
	}
	if len(unbound) > 0 {
		return &AuditGovernanceUnboundBacklogError{tenantIDs: unbound}
	}
	if err := s.updateGovernanceControl(ctx, tx, revision, digest); err != nil {
		return err
	}
	return tx.Commit()
}

func validGovernanceBindingStates(bindings []AuditGovernanceBindingState) bool {
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding.TenantID == "" ||
			(binding.State != AuditGovernanceBindingActive &&
				binding.State != AuditGovernanceBindingDraining) {
			return false
		}
		if _, exists := seen[binding.TenantID]; exists {
			return false
		}
		seen[binding.TenantID] = struct{}{}
	}
	return true
}

func lockGovernanceControl(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE audit_governance_control
SET updated_at_ns=updated_at_ns WHERE singleton`)
	return err
}

func governanceControl(ctx context.Context, tx *sql.Tx) (uint64, string, error) {
	var revision uint64
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT revision,desired_digest
FROM audit_governance_control WHERE singleton`).Scan(&revision, &digest)
	return revision, digest, err
}

func (s *sqlStore) replaceGovernanceBindings(
	ctx context.Context, tx *sql.Tx, revision uint64, desired []AuditGovernanceBindingState,
) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM audit_governance_bindings`); err != nil {
		return err
	}
	now := time.Now().UTC().UnixNano()
	for _, binding := range desired {
		_, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO audit_governance_bindings
(tenant_id,state,revision,updated_at_ns) VALUES ($1,$2,$3,$4)`),
			binding.TenantID, binding.State, revision, now)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *sqlStore) unboundGovernanceBacklog(
	ctx context.Context, tx *sql.Tx,
) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT o.tenant_id
FROM audit_governance_outbox o LEFT JOIN audit_governance_bindings b
 ON b.tenant_id=o.tenant_id
WHERE o.delivered_at_ns=0 AND b.tenant_id IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tenants []string
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			return nil, err
		}
		tenants = append(tenants, tenant)
	}
	sort.Strings(tenants)
	return tenants, rows.Err()
}

func (s *sqlStore) updateGovernanceControl(
	ctx context.Context, tx *sql.Tx, revision uint64, digest string,
) error {
	_, err := tx.ExecContext(ctx, s.rebind(`UPDATE audit_governance_control
SET revision=$1,desired_digest=$2,updated_at_ns=$3 WHERE singleton`),
		revision, digest, time.Now().UTC().UnixNano())
	return err
}

func (s *sqlStore) governanceCaptureActive(
	ctx context.Context, tx *sql.Tx, tenant string,
) (bool, error) {
	query := `SELECT state FROM audit_governance_bindings WHERE tenant_id=$1`
	if s.dialect == dialectPostgres {
		query += ` FOR KEY SHARE`
	}
	var state string
	err := tx.QueryRowContext(ctx, s.rebind(query), tenant).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return state == AuditGovernanceBindingActive, err
}

func (s *sqlStore) AuditGovernanceCanDisable(ctx context.Context) (bool, error) {
	var safe bool
	err := s.db.QueryRowContext(ctx, `SELECT
 NOT EXISTS (SELECT 1 FROM audit_governance_bindings) AND
 NOT EXISTS (SELECT 1 FROM audit_governance_outbox WHERE delivered_at_ns=0)`).Scan(&safe)
	return safe, err
}

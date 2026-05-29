package repository

import (
	"context"
	"time"
)

const auditCols = `id, created_at, actor, action, target, tenant_id, detail`

// RecordAudit appends one audit-log entry. id is assigned by the database; if
// the caller leaves CreatedAt empty it is stamped with the current UTC time.
func (s *sqlStore) RecordAudit(ctx context.Context, e AuditEntry) error {
	if e.CreatedAt == "" {
		e.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, s.rebind(
		`INSERT INTO audit_log (created_at, actor, action, target, tenant_id, detail)
		 VALUES ($1,$2,$3,$4,$5,$6)`),
		e.CreatedAt, e.Actor, e.Action, e.Target, e.TenantID, e.Detail)
	return err
}

// ListAudit returns audit entries newest-first. limit defaults to 100 and is
// clamped to 1000; the result is always a non-nil slice.
func (s *sqlStore) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT `+auditCols+` FROM audit_log ORDER BY id DESC LIMIT $1`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]AuditEntry, 0)
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.Actor, &e.Action, &e.Target, &e.TenantID, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

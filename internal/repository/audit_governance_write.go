package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type governanceExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *sqlStore) RecordAuditWithGovernance(
	ctx context.Context, entry AuditEntry, fact AuditGovernanceFact,
) error {
	if entry.CreatedAt == "" {
		entry.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(ctx, s.rebind(`INSERT INTO audit_log
(created_at, actor, action, target, tenant_id, detail) VALUES ($1,$2,$3,$4,$5,$6)
RETURNING id`), entry.CreatedAt, entry.Actor, entry.Action, entry.Target, entry.TenantID, entry.Detail)
	if err := row.Scan(&fact.OriginID); err != nil {
		return err
	}
	fact.OriginKind = AuditOriginAdmin
	// REQ-2: the ID's time bucket derives from the durably stored origin
	// timestamp (the exact string the gap path will parse), so the atomic
	// path and gap reconcile converge on the same bucket (E8 drift).
	if t, err := time.Parse(time.RFC3339Nano, entry.CreatedAt); err == nil && !t.IsZero() {
		fact.OccurredAt = t
	}
	fact.ID = DeterministicFactID(fact.SourceID, defaultTenant(fact.TenantID),
		fact.Action, fact.OriginKind, fact.OriginID, fact.OccurredAt)
	active, err := s.governanceCaptureActive(ctx, tx, fact.TenantID)
	if err != nil {
		return err
	}
	if active {
		err = s.insertAuditGovernance(ctx, tx, fact, false)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqlStore) InsertEventWithGovernance(
	ctx context.Context, event Event, fact AuditGovernanceFact,
) (int64, error) {
	event.TenantID = defaultTenant(event.TenantID)
	payload, oid, err := governanceEventValues(event)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	query := `INSERT INTO object_events
(tenant_id,bucket,key,type,object_id,request_id,payload) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id, created_at`
	if s.dialect == dialectPostgres {
		query = `INSERT INTO object_events
(tenant_id,bucket,key,type,object_id,request_id,payload) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb) RETURNING id, created_at`
	}
	var id int64
	var occurred flexTime
	err = tx.QueryRowContext(ctx, s.rebind(query), event.TenantID, event.Bucket, event.Key,
		string(event.Type), oid, event.RequestID, string(payload)).Scan(&id, &occurred)
	if err != nil {
		return 0, err
	}
	fact.OriginKind, fact.OriginID = AuditOriginFile, id
	// REQ-2: canonicalize occurred to the DB-default created_at (sqlite ms /
	// postgres µs) — byte-identical to what the gap path will parse, so the
	// two paths converge on the same time bucket.
	fact.OccurredAt = occurred.Time
	fact.ID = DeterministicFactID(fact.SourceID, defaultTenant(fact.TenantID),
		fact.Action, fact.OriginKind, fact.OriginID, fact.OccurredAt)
	active, err := s.governanceCaptureActive(ctx, tx, fact.TenantID)
	if err != nil {
		return 0, err
	}
	if active {
		err = s.insertAuditGovernance(ctx, tx, fact, false)
	}
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func governanceEventValues(event Event) ([]byte, any, error) {
	payload, err := jsonOrEmpty(event.Payload)
	if err != nil {
		return nil, nil, err
	}
	var oid any
	if event.ObjectID != nil {
		oid = *event.ObjectID
	}
	return payload, oid, nil
}

func (s *sqlStore) EnqueueAuditGovernance(
	ctx context.Context, fact AuditGovernanceFact,
) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	active, err := s.governanceCaptureActive(ctx, tx, fact.TenantID)
	if err != nil || !active {
		return false, err
	}
	// Store-authoritative: recompute the deterministic ID from the fact's
	// final fields (idempotent with factFromGap's computation; overwrites any
	// caller-set ID so the store is the single authority).
	fact.ID = DeterministicFactID(fact.SourceID, defaultTenant(fact.TenantID),
		fact.Action, fact.OriginKind, fact.OriginID, fact.OccurredAt)
	result, err := s.insertAuditGovernanceResult(ctx, tx, fact, true)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, tx.Commit()
}

func (s *sqlStore) insertAuditGovernance(
	ctx context.Context, exec governanceExecutor, fact AuditGovernanceFact, ignoreDuplicate bool,
) error {
	_, err := s.insertAuditGovernanceResult(ctx, exec, fact, ignoreDuplicate)
	return err
}

func (s *sqlStore) insertAuditGovernanceResult(
	ctx context.Context, exec governanceExecutor, fact AuditGovernanceFact, ignoreDuplicate bool,
) (sql.Result, error) {
	if err := validateAuditGovernanceFact(fact); err != nil {
		return nil, err
	}
	now := time.Now().UTC().UnixNano()
	query := `INSERT INTO audit_governance_outbox
(id,tenant_id,origin_kind,origin_id,fact_kind,actor_digest,action,target_digest,
request_id,detail_sha256,object_size_bytes,storage_backend,occurred_at_ns,available_at_ns,created_at_ns)
SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15
WHERE NOT EXISTS (SELECT 1 FROM audit_governance_delivered_origins
	WHERE origin_kind=$16 AND origin_id=$17)
AND NOT EXISTS (SELECT 1 FROM audit_governance_rejected_origins
	WHERE origin_kind=$18 AND origin_id=$19)`
	if ignoreDuplicate {
		query += ` ON CONFLICT (origin_kind,origin_id) DO NOTHING`
	}
	return exec.ExecContext(ctx, s.rebind(query), fact.ID, fact.TenantID, fact.OriginKind,
		fact.OriginID, fact.FactKind, fact.ActorDigest, fact.Action, fact.TargetDigest,
		fact.RequestID, fact.DetailSHA256, fact.ObjectSizeBytes, fact.StorageBackend,
		fact.OccurredAt.UTC().UnixNano(), now, now, fact.OriginKind, fact.OriginID,
		fact.OriginKind, fact.OriginID)
}

func validateAuditGovernanceFact(fact AuditGovernanceFact) error {
	if !validAuditGovernanceIdentity(fact) {
		return errors.New("audit governance fact identity is invalid")
	}
	if !validAuditGovernanceKinds(fact) {
		return errors.New("audit governance origin is invalid")
	}
	if !validAuditGovernanceValues(fact) {
		return errors.New("audit governance fact value is invalid")
	}
	return nil
}

func validAuditGovernanceIdentity(fact AuditGovernanceFact) bool {
	return fact.ID != "" && fact.TenantID != "" && fact.OriginID > 0 &&
		fact.Action != "" && !fact.OccurredAt.IsZero()
}

func validAuditGovernanceKinds(fact AuditGovernanceFact) bool {
	originOK := fact.OriginKind == AuditOriginAdmin || fact.OriginKind == AuditOriginFile
	kindOK := fact.FactKind == "admin" || fact.FactKind == "security" || fact.FactKind == "file"
	return originOK && kindOK
}

func validAuditGovernanceValues(fact AuditGovernanceFact) bool {
	return fact.ObjectSizeBytes >= 0 && len(fact.Action) <= 128 && len(fact.StorageBackend) <= 32
}

func (s *sqlStore) ListAuditGovernanceGaps(
	ctx context.Context, tenant string, limit int,
) ([]AuditGovernanceGap, error) {
	if tenant == "" {
		return nil, errors.New("audit governance tenant is required")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	audits, err := s.listGovernanceAuditGaps(ctx, tenant, limit)
	if err != nil {
		return nil, err
	}
	events, err := s.listGovernanceEventGaps(ctx, tenant, limit)
	if err != nil {
		return nil, err
	}
	return mergeGovernanceGaps(audits, events, limit), nil
}

func mergeGovernanceGaps(
	audits, events []AuditGovernanceGap, limit int,
) []AuditGovernanceGap {
	gaps := make([]AuditGovernanceGap, 0, min(limit, len(audits)+len(events)))
	for index := 0; len(gaps) < limit; index++ {
		added := false
		if index < len(audits) {
			gaps, added = append(gaps, audits[index]), true
		}
		if index < len(events) && len(gaps) < limit {
			gaps, added = append(gaps, events[index]), true
		}
		if !added {
			break
		}
	}
	return gaps
}

func (s *sqlStore) listGovernanceAuditGaps(
	ctx context.Context, tenant string, limit int,
) ([]AuditGovernanceGap, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT a.id,a.tenant_id,a.actor,a.action,a.target,a.detail,a.created_at
FROM audit_log a LEFT JOIN audit_governance_outbox o
 ON o.origin_kind='admin' AND o.origin_id=a.id
LEFT JOIN audit_governance_delivered_origins d
 ON d.origin_kind='admin' AND d.origin_id=a.id
LEFT JOIN audit_governance_rejected_origins r
 ON r.origin_kind='admin' AND r.origin_id=a.id
WHERE a.tenant_id=$1 AND o.id IS NULL AND d.origin_id IS NULL AND r.origin_id IS NULL
ORDER BY a.id LIMIT $2`), tenant, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	gaps := make([]AuditGovernanceGap, 0)
	for rows.Next() {
		var gap AuditGovernanceGap
		var occurred string
		gap.OriginKind = AuditOriginAdmin
		if err := rows.Scan(&gap.OriginID, &gap.TenantID, &gap.Actor, &gap.Action,
			&gap.Target, &gap.Detail, &occurred); err != nil {
			return nil, err
		}
		gap.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurred)
		gaps = append(gaps, gap)
	}
	return gaps, rows.Err()
}

func (s *sqlStore) listGovernanceEventGaps(
	ctx context.Context, tenant string, limit int,
) ([]AuditGovernanceGap, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT e.id,e.tenant_id,e.bucket,e.key,e.type,e.request_id,e.payload,e.created_at
FROM object_events e LEFT JOIN audit_governance_outbox o
 ON o.origin_kind='file' AND o.origin_id=e.id
LEFT JOIN audit_governance_delivered_origins d
 ON d.origin_kind='file' AND d.origin_id=e.id
LEFT JOIN audit_governance_rejected_origins r
 ON r.origin_kind='file' AND r.origin_id=e.id
WHERE e.tenant_id=$1 AND o.id IS NULL AND d.origin_id IS NULL AND r.origin_id IS NULL
ORDER BY e.id LIMIT $2`), tenant, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	gaps := make([]AuditGovernanceGap, 0)
	for rows.Next() {
		var gap AuditGovernanceGap
		var payload []byte
		var occurred flexTime
		gap.OriginKind = AuditOriginFile
		if err := rows.Scan(&gap.OriginID, &gap.TenantID, &gap.Bucket, &gap.Key,
			&gap.Action, &gap.RequestID, &payload, &occurred); err != nil {
			return nil, err
		}
		gap.Action, gap.OccurredAt = "file."+gap.Action, occurred.Time
		gap.Payload, _ = unmarshalKV(payload)
		gaps = append(gaps, gap)
	}
	return gaps, rows.Err()
}

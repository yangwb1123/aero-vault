package repository

import (
	"context"
	"database/sql"
	"time"
)

func (s *sqlStore) InsertEvent(ctx context.Context, e Event) (int64, error) {
	e.TenantID = defaultTenant(e.TenantID)
	payload, err := jsonOrEmpty(e.Payload)
	if err != nil {
		return 0, err
	}
	var oid any = nil
	if e.ObjectID != nil {
		oid = *e.ObjectID
	}
	var (
		q   string
		row *sql.Row
	)
	if s.dialect == dialectPostgres {
		q = `INSERT INTO object_events (tenant_id, bucket, key, type, object_id, request_id, payload) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb) RETURNING id`
	} else {
		q = `INSERT INTO object_events (tenant_id, bucket, key, type, object_id, request_id, payload) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`
	}
	row = s.db.QueryRowContext(ctx, s.rebind(q), e.TenantID, e.Bucket, e.Key, string(e.Type), oid, e.RequestID, string(payload))
	var id int64
	if err := row.Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *sqlStore) NextUnconsumedEvents(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 32
	}
	q := `SELECT id, tenant_id, bucket, key, type, object_id, request_id, payload, created_at
FROM object_events WHERE consumed_at IS NULL ORDER BY id ASC LIMIT $1`
	rows, err := s.db.QueryContext(ctx, s.rebind(q), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var (
			e        Event
			oid      sql.NullInt64
			payload  []byte
			createdT flexTime
			typeStr  string
		)
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Bucket, &e.Key, &typeStr, &oid, &e.RequestID, &payload, &createdT); err != nil {
			return nil, err
		}
		e.Type = EventType(typeStr)
		if oid.Valid {
			v := oid.Int64
			e.ObjectID = &v
		}
		e.CreatedAt = createdT.Time
		e.Payload, _ = unmarshalKV(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *sqlStore) MarkEventConsumed(ctx context.Context, id int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var q string
	if s.dialect == dialectPostgres {
		q = `UPDATE object_events SET consumed_at=now() WHERE id=$1`
		_, err := s.db.ExecContext(ctx, s.rebind(q), id)
		return err
	}
	q = `UPDATE object_events SET consumed_at=$1 WHERE id=$2`
	_, err := s.db.ExecContext(ctx, s.rebind(q), now, id)
	return err
}

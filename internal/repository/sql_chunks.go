package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

func (s *sqlStore) DeleteChunksForObject(ctx context.Context, objectID int64) error {
	_, err := s.db.ExecContext(ctx, s.rebind(`DELETE FROM chunks WHERE object_id=$1`), objectID)
	return err
}

func (s *sqlStore) InsertChunks(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.rebind(`INSERT INTO chunks (object_id, tenant_id, bucket, object_key, seq, content, embedding, dim, embed_model) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`)
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range chunks {
		c.TenantID = defaultTenant(c.TenantID)
		blob := encodeEmbedding(c.Embedding)
		if _, err := stmt.ExecContext(ctx, c.ObjectID, c.TenantID, c.Bucket, c.ObjectKey, c.Seq, c.Content, blob, c.Dim, c.EmbedModel); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *sqlStore) ListChunksForObject(ctx context.Context, objectID int64) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT id, object_id, tenant_id, bucket, object_key, seq, content, embedding, dim, embed_model, created_at FROM chunks WHERE object_id=$1 ORDER BY seq ASC`), objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunks(rows)
}

// ListObjectIDsToReindex returns distinct object IDs whose chunks were embedded
// by a model other than currentModel — i.e. stale after the embedder changed —
// so they can be re-indexed against the active model.
func (s *sqlStore) ListObjectIDsToReindex(ctx context.Context, tenant, currentModel string, limit int) ([]int64, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT DISTINCT object_id FROM chunks WHERE tenant_id=$1 AND (embed_model = '' OR embed_model <> $2) LIMIT $3`),
		tenant, currentModel, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *sqlStore) SearchChunks(ctx context.Context, tenant, bucket string, query []float32, limit int) ([]SearchHit, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	q := `SELECT id, object_id, tenant_id, bucket, object_key, seq, content, embedding, dim, embed_model, created_at
FROM chunks WHERE tenant_id=$1 AND embedding IS NOT NULL`
	args := []any{tenant}
	if bucket != "" {
		q += ` AND bucket=$2`
		args = append(args, bucket)
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all, err := scanChunks(rows)
	if err != nil {
		return nil, err
	}
	hits := make([]SearchHit, 0, len(all))
	qNorm := norm(query)
	for _, c := range all {
		if len(c.Embedding) != len(query) || qNorm == 0 {
			continue
		}
		score := cosine(query, c.Embedding, qNorm)
		hits = append(hits, SearchHit{Chunk: c, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// --- Audit ---

func (s *sqlStore) RecordUsage(ctx context.Context, u Usage) error {
	u.TenantID = defaultTenant(u.TenantID)
	cidBytes, _ := json.Marshal(u.ChunkIDs)
	oidBytes, _ := json.Marshal(u.ObjectIDs)
	var q string
	if s.dialect == dialectPostgres {
		q = `INSERT INTO ai_usage (tenant_id, caller, query, chunk_ids, object_ids, request_id, model, prompt_tokens, completion_tokens, total_tokens, latency_ms, cost_micros) VALUES ($1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,$8,$9,$10,$11,$12)`
	} else {
		q = `INSERT INTO ai_usage (tenant_id, caller, query, chunk_ids, object_ids, request_id, model, prompt_tokens, completion_tokens, total_tokens, latency_ms, cost_micros) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`
	}
	_, err := s.db.ExecContext(ctx, s.rebind(q), u.TenantID, u.Caller, u.Query, string(cidBytes), string(oidBytes), u.RequestID, u.Model, u.PromptTokens, u.CompletionTokens, u.TotalTokens, u.LatencyMs, u.CostMicros)
	return err
}

func (s *sqlStore) SumAICostMicros(ctx context.Context, tenant, since string) (int64, error) {
	tenant = defaultTenant(tenant)
	q := `SELECT COALESCE(SUM(cost_micros), 0) FROM ai_usage WHERE tenant_id = $1 AND created_at >= $2`
	var total int64
	if err := s.db.QueryRowContext(ctx, s.rebind(q), tenant, since).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (s *sqlStore) ListUsageForObject(ctx context.Context, tenant string, objectID int64, limit int) ([]Usage, error) {
	rows, err := s.queryUsageForObject(ctx, tenant, objectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if s.dialect == dialectPostgres {
		return scanUsages(rows)
	}
	all, err := scanUsages(rows)
	if err != nil {
		return nil, err
	}
	out := make([]Usage, 0, limit)
	for _, u := range all {
		for _, id := range u.ObjectIDs {
			if id == objectID {
				out = append(out, u)
				break
			}
		}
		if len(out) >= limit {
			break
		}
	}
	return out, rows.Err()
}

func (s *sqlStore) queryUsageForObject(ctx context.Context, tenant string, objectID int64, limit int) (*sql.Rows, error) {
	tenant = defaultTenant(tenant)
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	if s.dialect == dialectPostgres {
		return s.queryObjectUsagePG(ctx, tenant, objectID, limit)
	}
	return s.queryObjectUsageSQLite(ctx, tenant, objectID, limit)
}

func (s *sqlStore) queryObjectUsagePG(ctx context.Context, tenant string, objectID int64, limit int) (*sql.Rows, error) {
	q := `SELECT id, tenant_id, caller, query, chunk_ids, object_ids, request_id, created_at, model, prompt_tokens, completion_tokens, total_tokens, latency_ms, cost_micros
FROM ai_usage WHERE tenant_id=$1 AND object_ids @> $2::jsonb ORDER BY id DESC LIMIT $3`
	oidJSON := fmt.Sprintf("[%d]", objectID)
	return s.db.QueryContext(ctx, s.rebind(q), tenant, oidJSON, limit)
}

func (s *sqlStore) queryObjectUsageSQLite(ctx context.Context, tenant string, objectID int64, limit int) (*sql.Rows, error) {
	q := `SELECT id, tenant_id, caller, query, chunk_ids, object_ids, request_id, created_at, model, prompt_tokens, completion_tokens, total_tokens, latency_ms, cost_micros
FROM ai_usage WHERE tenant_id=$1 ORDER BY id DESC LIMIT $2`
	return s.db.QueryContext(ctx, s.rebind(q), tenant, limit*4)
}

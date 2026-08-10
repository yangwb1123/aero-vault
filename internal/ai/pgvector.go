// Package ai: pgvector-backed VectorIndex adapter.
//
// PgVectorIndex implements VectorIndex using PostgreSQL's pgvector extension
// for approximate-nearest-neighbour (ANN) search (HNSW or IVFFlat indexes),
// replacing the default brute-force scan for large corpora. It runs a
// cosine-distance query (the `<=>` operator) ordered by distance and converts
// the result back into the SAME repository.SearchHit/repository.Chunk shapes
// the brute-force path returns, so downstream Search code is unchanged.
//
// REQUIREMENTS / CAVEATS:
//
//   - This backend is Postgres-only. It REQUIRES a PostgreSQL database with the
//     `vector` extension installed (CREATE EXTENSION vector) and a populated
//     `vector`-typed column on the chunks table (default name: embedding_vec).
//   - It is OPT-IN: nothing wires it in by default. An operator constructs it
//     explicitly (NewPgVectorIndex / OpenPgVectorIndex) and swaps it onto Search
//     via Search.WithVectorIndex(vi).
//   - It is NOT exercised by CI tests: there is no Postgres in the test harness,
//     so SearchVectors' runtime behaviour against a live database is UNVERIFIED.
//     Only structural concerns (interface conformance, the empty-DSN guard, and
//     the vector literal formatting) are unit-tested in pgvector_test.go.
//
// POPULATING THE VECTOR COLUMN:
//
// The default `chunks` schema stores embeddings as raw BYTEA in the `embedding`
// column. pgvector needs a native `vector` column plus an ANN index. Do NOT add
// this as a repository migration in this repo — migrations also run against
// SQLite in CI, where `vector` and `hnsw` are unknown and would fail. Instead an
// operator runs a Postgres-only migration out of band, e.g.:
//
//	CREATE EXTENSION IF NOT EXISTS vector;
//	ALTER TABLE chunks ADD COLUMN IF NOT EXISTS embedding_vec vector(<dim>);
//	-- backfill embedding_vec from the existing embedding bytes (one-off job),
//	-- then build the ANN index for cosine distance:
//	CREATE INDEX IF NOT EXISTS chunks_embedding_vec_hnsw
//	  ON chunks USING hnsw (embedding_vec vector_cosine_ops);
//	CREATE INDEX IF NOT EXISTS chunks_tenant_bucket_idx
//	  ON chunks (tenant_id, bucket);
//
// where <dim> is the embedding dimension produced by the configured model.
package ai

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx stdlib driver, registered as "pgx"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// var _ asserts PgVectorIndex satisfies the VectorIndex contract.
var _ VectorIndex = (*PgVectorIndex)(nil)

// PgVectorOptions configures the table/column identifiers PgVectorIndex queries.
// These are operator configuration (not user input); they are injected into the
// query via fmt.Sprintf after validation, while all values use $N placeholders.
type PgVectorOptions struct {
	// Table is the table holding chunk rows. Default: "chunks".
	Table string
	// VectorColumn is the pgvector `vector`-typed column. Default: "embedding_vec".
	VectorColumn string
}

func (o PgVectorOptions) withDefaults() PgVectorOptions {
	if o.Table == "" {
		o.Table = "chunks"
	}
	if o.VectorColumn == "" {
		o.VectorColumn = "embedding_vec"
	}
	return o
}

// PgVectorIndex is a VectorIndex backed by pgvector ANN search over its own
// *sql.DB. The operator points db at a Postgres database whose chunks table has
// a populated `vector` column (see package doc).
type PgVectorIndex struct {
	db   *sql.DB
	opts PgVectorOptions
}

// NewPgVectorIndex wraps an already-open *sql.DB. The caller owns db's lifecycle.
func NewPgVectorIndex(db *sql.DB, opts PgVectorOptions) *PgVectorIndex {
	return &PgVectorIndex{db: db, opts: opts.withDefaults()}
}

// OpenPgVectorIndex opens a Postgres connection using the same driver name
// (pgx) the repository uses, pings it, and returns a ready PgVectorIndex. The
// returned index owns the *sql.DB. It returns an error on an empty DSN.
func OpenPgVectorIndex(ctx context.Context, dsn string, opts PgVectorOptions) (*PgVectorIndex, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("pgvector: empty dsn")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("pgvector: open postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pgvector: ping postgres: %w", err)
	}
	return NewPgVectorIndex(db, opts), nil
}

// Close releases the underlying *sql.DB. Safe to call when db is nil.
func (p *PgVectorIndex) Close() error {
	if p == nil || p.db == nil {
		return nil
	}
	return p.db.Close()
}

// SearchVectors runs a pgvector cosine-distance ANN query for the given tenant
// (and optional bucket) and returns the top-`limit` chunks as repository
// SearchHits, ordered by descending similarity. Score is 1 - cosine_distance.
// Limit is clamped like QdrantIndex (<=0 -> default 10; >100 -> capped at 100).
func (p *PgVectorIndex) SearchVectors(ctx context.Context, tenant, bucket string, query []float32, limit int) ([]repository.SearchHit, error) {
	limit = clampSearchLimit(limit)

	// Table/column come from operator config, not user input; inject via
	// Sprintf. All runtime values are bound with distinct $N placeholders
	// (no placeholder is reused).
	q := fmt.Sprintf(`SELECT id, tenant_id, bucket, object_id, object_key, seq, content, dim, embed_model, 1 - (%[1]s <=> $1) AS score
FROM %[2]s
WHERE tenant_id=$2 AND ($3='' OR bucket=$3) AND %[1]s IS NOT NULL
ORDER BY %[1]s <=> $1
LIMIT $4`, p.opts.VectorColumn, p.opts.Table)

	rows, err := p.db.QueryContext(ctx, q, vectorLiteral(query), tenant, bucket, limit)
	if err != nil {
		return nil, fmt.Errorf("pgvector: query: %w", err)
	}
	defer rows.Close()

	var hits []repository.SearchHit
	for rows.Next() {
		var (
			c     repository.Chunk
			score float64
		)
		if err := rows.Scan(
			&c.ID,
			&c.TenantID,
			&c.Bucket,
			&c.ObjectID,
			&c.ObjectKey,
			&c.Seq,
			&c.Content,
			&c.Dim,
			&c.EmbedModel,
			&score,
		); err != nil {
			return nil, fmt.Errorf("pgvector: scan: %w", err)
		}
		hits = append(hits, repository.SearchHit{Chunk: c, Score: float32(score)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgvector: rows: %w", err)
	}
	return hits, nil
}

// vectorLiteral formats a query vector as a pgvector text literal, e.g.
// []float32{1,2,3} -> "[1,2,3]". pgvector accepts this textual form for a
// bound parameter of `vector` type. Floats use the shortest round-trippable
// representation ('g', -1 precision).
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

package ai

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aero-vault/aero-vault/internal/repository"
)

// LexicalIndex is the seam for keyword/lexical retrieval, mirroring VectorIndex.
// The default path uses the in-process BM25 index (rebuilt per replica). A
// Postgres FTS adapter (PgFTSIndex) implements the same contract so lexical
// search can run in the database (shared, incremental) instead of in app memory
// — the scale fix called for in ROADMAP #1.
type LexicalIndex interface {
	SearchLexical(ctx context.Context, tenant, bucket, query string, limit int) ([]repository.SearchHit, error)
}

// PgFTSIndex is a Postgres full-text-search lexical backend over the `chunks`
// table's `content` column using tsvector/tsquery + ts_rank.
//
// OPT-IN and Postgres-only. It REQUIRES a Postgres database (ideally with a
// GIN index on `to_tsvector('english', content)` for performance). It is NOT
// exercised by CI tests — there is no Postgres in the harness — so its query
// execution is UNVERIFIED here; only compilation and structural tests run.
//
// Suggested out-of-band index (not added as a migration, since CI runs SQLite):
//
//	CREATE INDEX chunks_content_fts_idx ON chunks
//	  USING gin (to_tsvector('english', content));
type PgFTSIndex struct {
	db     *sql.DB
	table  string
	ownsDB bool
	tsLang string
}

// PgFTSOptions configures the FTS adapter.
type PgFTSOptions struct {
	Table    string // default "chunks"
	Language string // default "english"
}

func (o PgFTSOptions) withDefaults() PgFTSOptions {
	if o.Table == "" {
		o.Table = "chunks"
	}
	if o.Language == "" {
		o.Language = "english"
	}
	return o
}

// NewPgFTSIndex wraps an existing *sql.DB (caller owns it).
func NewPgFTSIndex(db *sql.DB, opts PgFTSOptions) *PgFTSIndex {
	o := opts.withDefaults()
	return &PgFTSIndex{db: db, table: o.Table, tsLang: o.Language}
}

// OpenPgFTSIndex opens its own Postgres connection from dsn (using the same
// driver name the repository uses).
func OpenPgFTSIndex(ctx context.Context, dsn string, opts PgFTSOptions) (*PgFTSIndex, error) {
	if dsn == "" {
		return nil, errors.New("pgfts: empty dsn")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("pgfts: open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pgfts: ping: %w", err)
	}
	idx := NewPgFTSIndex(db, opts)
	idx.ownsDB = true
	return idx, nil
}

// Close closes the underlying DB if this index opened it.
func (p *PgFTSIndex) Close() error {
	if p.ownsDB && p.db != nil {
		return p.db.Close()
	}
	return nil
}

func (p *PgFTSIndex) SearchLexical(ctx context.Context, tenant, bucket, query string, limit int) ([]repository.SearchHit, error) {
	q := fmt.Sprintf(`SELECT id, tenant_id, bucket, object_id, object_key, seq, content, dim, embed_model,
ts_rank(to_tsvector('%[1]s', content), plainto_tsquery('%[1]s', $1)) AS score
FROM %[2]s
WHERE tenant_id=$2 AND ($3='' OR bucket=$3)
  AND to_tsvector('%[1]s', content) @@ plainto_tsquery('%[1]s', $1)
ORDER BY score DESC
LIMIT $4`, p.tsLang, p.table)

	rows, err := p.db.QueryContext(ctx, q, query, tenant, bucket, limit)
	if err != nil {
		return nil, fmt.Errorf("pgfts query: %w", err)
	}
	defer rows.Close()

	var out []repository.SearchHit
	for rows.Next() {
		var (
			c     repository.Chunk
			score float64
		)
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Bucket, &c.ObjectID, &c.ObjectKey, &c.Seq, &c.Content, &c.Dim, &c.EmbedModel, &score); err != nil {
			return nil, fmt.Errorf("pgfts scan: %w", err)
		}
		out = append(out, repository.SearchHit{Chunk: c, Score: float32(score)})
	}
	return out, rows.Err()
}

var _ LexicalIndex = (*PgFTSIndex)(nil)

-- Operator setup for the OPT-IN Postgres-backed AI retrieval adapters
-- (AI_VECTOR_BACKEND=pgvector / AI_LEXICAL_BACKEND=pgfts). Apply this to your
-- aero-vault Postgres AFTER the app's own migrations have created `chunks`.
--
-- This is intentionally NOT a core migration: it requires the pgvector
-- extension and would break plain-Postgres deployments that don't use it, so it
-- is applied deliberately by the operator. Set <DIM> to your embedding
-- dimension (AI_EMBED_DIM, default 256).
--
-- Verified end-to-end by internal/integration (make test-integration).

-- 1. Vector column + HNSW index for pgvector ANN search (PgVectorIndex).
CREATE EXTENSION IF NOT EXISTS vector;
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS embedding_vec vector(256); -- <DIM>
CREATE INDEX IF NOT EXISTS chunks_vec_hnsw_idx
  ON chunks USING hnsw (embedding_vec vector_cosine_ops);

-- Backfill embedding_vec from the existing BYTEA `embedding` column is
-- application-specific (decode float32le → vector literal); new chunks should
-- be written with embedding_vec populated. Until then, rows with a NULL
-- embedding_vec are simply skipped by the ANN query.

-- 2. GIN index for Postgres full-text lexical search (PgFTSIndex).
CREATE INDEX IF NOT EXISTS chunks_content_fts_idx
  ON chunks USING gin (to_tsvector('english', content));

-- 3. The LISTEN/NOTIFY event transport (EVENTS_TRANSPORT=postgres) needs no
--    schema — it uses pg_notify on a channel (default "aero_vault_events").

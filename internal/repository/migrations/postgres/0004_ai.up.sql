CREATE TABLE IF NOT EXISTS chunks (
  id          BIGSERIAL PRIMARY KEY,
  object_id   BIGINT NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
  tenant_id   TEXT NOT NULL,
  bucket      TEXT NOT NULL,
  object_key  TEXT NOT NULL,
  seq         INT  NOT NULL,
  content     TEXT NOT NULL,
  embedding   BYTEA,
  dim         INT  NOT NULL DEFAULT 0,
  embed_model TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS chunks_object_idx ON chunks (object_id, seq);
CREATE INDEX IF NOT EXISTS chunks_tenant_idx ON chunks (tenant_id, bucket);

CREATE TABLE IF NOT EXISTS ai_usage (
  id         BIGSERIAL PRIMARY KEY,
  tenant_id  TEXT NOT NULL,
  caller     TEXT NOT NULL,
  query      TEXT NOT NULL DEFAULT '',
  chunk_ids  JSONB NOT NULL DEFAULT '[]'::jsonb,
  object_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
  request_id TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ai_usage_tenant_idx ON ai_usage (tenant_id, created_at);
CREATE INDEX IF NOT EXISTS ai_usage_object_idx ON ai_usage (tenant_id, created_at, caller);

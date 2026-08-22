CREATE TABLE IF NOT EXISTS bucket_access_logs (
  id            BIGSERIAL PRIMARY KEY,
  created_at    TEXT NOT NULL,
  tenant_id     TEXT NOT NULL,
  source_bucket TEXT NOT NULL,
  method        TEXT NOT NULL,
  object_key    TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT '',
  latency_ms    TEXT NOT NULL DEFAULT '',
  user_agent    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS bucket_access_logs_lookup_idx
  ON bucket_access_logs (tenant_id, source_bucket, created_at);

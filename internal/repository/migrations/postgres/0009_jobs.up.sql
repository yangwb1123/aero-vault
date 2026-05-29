CREATE TABLE IF NOT EXISTS jobs (
  id           BIGSERIAL PRIMARY KEY,
  tenant_id    TEXT NOT NULL DEFAULT 'default',
  type         TEXT NOT NULL,
  payload      TEXT NOT NULL DEFAULT '{}',
  status       TEXT NOT NULL DEFAULT 'pending',   -- pending | running | succeeded | failed
  priority     INT  NOT NULL DEFAULT 0,           -- higher runs first
  attempts     INT  NOT NULL DEFAULT 0,
  max_attempts INT  NOT NULL DEFAULT 5,
  run_after    TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_error   TEXT NOT NULL DEFAULT '',
  worker       TEXT NOT NULL DEFAULT '',
  result       TEXT NOT NULL DEFAULT '',
  dedupe_key   TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at   TIMESTAMPTZ,
  finished_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS jobs_claim_idx ON jobs (status, run_after, priority, id);
CREATE INDEX IF NOT EXISTS jobs_tenant_idx ON jobs (tenant_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS jobs_dedupe_idx ON jobs (dedupe_key) WHERE dedupe_key IS NOT NULL AND status IN ('pending', 'running');

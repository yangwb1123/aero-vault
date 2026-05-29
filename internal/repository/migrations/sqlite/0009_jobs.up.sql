CREATE TABLE jobs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  tenant_id    TEXT NOT NULL DEFAULT 'default',
  type         TEXT NOT NULL,
  payload      TEXT NOT NULL DEFAULT '{}',
  status       TEXT NOT NULL DEFAULT 'pending',   -- pending | running | succeeded | failed
  priority     INTEGER NOT NULL DEFAULT 0,        -- higher runs first
  attempts     INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 5,
  run_after    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  last_error   TEXT NOT NULL DEFAULT '',
  worker       TEXT NOT NULL DEFAULT '',
  result       TEXT NOT NULL DEFAULT '',
  dedupe_key   TEXT,
  created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  started_at   TEXT,
  finished_at  TEXT
);

-- Claim path: find the next runnable job by priority then age.
CREATE INDEX jobs_claim_idx ON jobs (status, run_after, priority, id);
CREATE INDEX jobs_tenant_idx ON jobs (tenant_id, created_at);
-- At most one live (pending/running) job per dedupe key.
CREATE UNIQUE INDEX jobs_dedupe_idx ON jobs (dedupe_key) WHERE dedupe_key IS NOT NULL AND status IN ('pending', 'running');

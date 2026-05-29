CREATE TABLE idempotency_keys (
  tenant_id       TEXT NOT NULL,
  idem_key        TEXT NOT NULL,
  fingerprint     TEXT NOT NULL,
  status          TEXT NOT NULL,            -- 'in_progress' | 'completed'
  response_status INTEGER NOT NULL DEFAULT 0,
  response_body   BLOB,
  response_ct     TEXT NOT NULL DEFAULT '',
  request_id      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  completed_at    TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, idem_key)
);

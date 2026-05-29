CREATE TABLE IF NOT EXISTS api_keys (
  token_hash   TEXT PRIMARY KEY,            -- sha256 hex of the token
  tenant_id    TEXT NOT NULL,
  scopes       TEXT NOT NULL DEFAULT '',    -- e.g. 'read+write' (opaque)
  label        TEXT NOT NULL DEFAULT '',    -- redacted hint / human name
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL DEFAULT '',    -- RFC3339, '' = no expiry
  last_used_at TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS api_keys_tenant_idx ON api_keys (tenant_id);

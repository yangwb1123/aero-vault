CREATE TABLE tenant_quotas (
  tenant_id        TEXT PRIMARY KEY,
  max_bytes        INTEGER NOT NULL DEFAULT 0,   -- 0 = unlimited
  max_objects      INTEGER NOT NULL DEFAULT 0,
  used_bytes       INTEGER NOT NULL DEFAULT 0,
  used_objects     INTEGER NOT NULL DEFAULT 0,
  updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

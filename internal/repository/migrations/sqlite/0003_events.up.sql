CREATE TABLE object_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  tenant_id  TEXT NOT NULL,
  bucket     TEXT NOT NULL,
  key        TEXT NOT NULL,
  type       TEXT NOT NULL,        -- created | deleted | accessed
  object_id  INTEGER,              -- nullable (deleted events may lose ref)
  request_id TEXT NOT NULL DEFAULT '',
  payload    TEXT NOT NULL DEFAULT '{}',
  consumed_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX object_events_unconsumed_idx ON object_events (consumed_at, id) WHERE consumed_at IS NULL;
CREATE INDEX object_events_tenant_idx ON object_events (tenant_id, created_at);

CREATE TABLE chunks (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  object_id   INTEGER NOT NULL,
  tenant_id   TEXT NOT NULL,
  bucket      TEXT NOT NULL,
  object_key  TEXT NOT NULL,
  seq         INTEGER NOT NULL,
  content     TEXT NOT NULL,
  embedding   BLOB,
  dim         INTEGER NOT NULL DEFAULT 0,
  embed_model TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  FOREIGN KEY (object_id) REFERENCES objects(id) ON DELETE CASCADE
);

CREATE INDEX chunks_object_idx  ON chunks (object_id, seq);
CREATE INDEX chunks_tenant_idx  ON chunks (tenant_id, bucket);

CREATE TABLE ai_usage (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  tenant_id   TEXT NOT NULL,
  caller      TEXT NOT NULL,         -- 'rest:search' | 'mcp:search' | 'mcp:read' ...
  query       TEXT NOT NULL DEFAULT '',
  chunk_ids   TEXT NOT NULL DEFAULT '[]',  -- JSON array
  object_ids  TEXT NOT NULL DEFAULT '[]',  -- JSON array
  request_id  TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX ai_usage_tenant_idx ON ai_usage (tenant_id, created_at);
CREATE INDEX ai_usage_object_idx ON ai_usage (tenant_id, created_at, caller);

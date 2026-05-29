CREATE TABLE objects (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  bucket       TEXT    NOT NULL DEFAULT 'default',
  key          TEXT    NOT NULL,
  backend      TEXT    NOT NULL,
  storage_key  TEXT    NOT NULL,
  size         INTEGER NOT NULL,
  etag         TEXT    NOT NULL,
  content_type TEXT    NOT NULL DEFAULT '',
  metadata     TEXT    NOT NULL DEFAULT '{}',
  tags         TEXT    NOT NULL DEFAULT '{}',
  created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  deleted_at   TEXT
);

CREATE UNIQUE INDEX objects_live_unique_idx ON objects (bucket, key) WHERE deleted_at IS NULL;
CREATE INDEX objects_bucket_prefix_idx ON objects (bucket, key);

CREATE TABLE buckets (
  name       TEXT PRIMARY KEY,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE multipart_uploads (
  upload_id   TEXT PRIMARY KEY,
  bucket      TEXT NOT NULL,
  key         TEXT NOT NULL,
  backend     TEXT NOT NULL,
  backend_uid TEXT NOT NULL,
  metadata    TEXT NOT NULL DEFAULT '{}',
  created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE multipart_parts (
  upload_id   TEXT    NOT NULL,
  part_number INTEGER NOT NULL,
  etag        TEXT    NOT NULL,
  size        INTEGER NOT NULL,
  PRIMARY KEY (upload_id, part_number),
  FOREIGN KEY (upload_id) REFERENCES multipart_uploads(upload_id) ON DELETE CASCADE
);

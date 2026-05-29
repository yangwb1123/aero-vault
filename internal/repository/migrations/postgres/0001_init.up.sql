CREATE TABLE IF NOT EXISTS objects (
  id           BIGSERIAL PRIMARY KEY,
  bucket       TEXT NOT NULL DEFAULT 'default',
  key          TEXT NOT NULL,
  backend      TEXT NOT NULL,
  storage_key  TEXT NOT NULL,
  size         BIGINT NOT NULL,
  etag         TEXT NOT NULL,
  content_type TEXT NOT NULL DEFAULT '',
  metadata     JSONB NOT NULL DEFAULT '{}'::jsonb,
  tags         JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at   TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS objects_live_unique_idx ON objects (bucket, key) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS objects_bucket_prefix_idx ON objects (bucket, key text_pattern_ops);

CREATE TABLE IF NOT EXISTS buckets (
  name       TEXT PRIMARY KEY,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS multipart_uploads (
  upload_id   TEXT PRIMARY KEY,
  bucket      TEXT NOT NULL,
  key         TEXT NOT NULL,
  backend     TEXT NOT NULL,
  backend_uid TEXT NOT NULL,
  metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS multipart_parts (
  upload_id   TEXT NOT NULL REFERENCES multipart_uploads(upload_id) ON DELETE CASCADE,
  part_number INT  NOT NULL,
  etag        TEXT NOT NULL,
  size        BIGINT NOT NULL,
  PRIMARY KEY (upload_id, part_number)
);

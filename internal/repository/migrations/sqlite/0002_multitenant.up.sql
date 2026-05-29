ALTER TABLE objects ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'default';
DROP INDEX IF EXISTS objects_live_unique_idx;
DROP INDEX IF EXISTS objects_bucket_prefix_idx;
CREATE UNIQUE INDEX objects_live_unique_idx ON objects (tenant_id, bucket, key) WHERE deleted_at IS NULL;
CREATE INDEX objects_bucket_prefix_idx ON objects (tenant_id, bucket, key);

CREATE TABLE buckets_new (
  tenant_id  TEXT NOT NULL DEFAULT 'default',
  name       TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  PRIMARY KEY (tenant_id, name)
);
INSERT INTO buckets_new (tenant_id, name, created_at) SELECT 'default', name, created_at FROM buckets;
DROP TABLE buckets;
ALTER TABLE buckets_new RENAME TO buckets;

ALTER TABLE multipart_uploads ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'default';

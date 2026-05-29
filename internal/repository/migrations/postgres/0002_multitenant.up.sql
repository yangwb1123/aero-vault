ALTER TABLE objects ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'default';
DROP INDEX IF EXISTS objects_live_unique_idx;
DROP INDEX IF EXISTS objects_bucket_prefix_idx;
CREATE UNIQUE INDEX objects_live_unique_idx ON objects (tenant_id, bucket, key) WHERE deleted_at IS NULL;
CREATE INDEX objects_bucket_prefix_idx ON objects (tenant_id, bucket, key text_pattern_ops);

ALTER TABLE buckets ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'default';
ALTER TABLE buckets DROP CONSTRAINT buckets_pkey;
ALTER TABLE buckets ADD PRIMARY KEY (tenant_id, name);

ALTER TABLE multipart_uploads ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'default';

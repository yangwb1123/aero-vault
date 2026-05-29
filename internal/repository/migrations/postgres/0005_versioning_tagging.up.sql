ALTER TABLE buckets ADD COLUMN versioning BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE buckets ADD COLUMN object_lock_seconds INTEGER NOT NULL DEFAULT 0;

ALTER TABLE objects ADD COLUMN version_id TEXT NOT NULL DEFAULT '';
ALTER TABLE objects ADD COLUMN locked_until TIMESTAMPTZ;
UPDATE objects SET version_id = id::text || '-' || floor(random()*1000000)::text WHERE version_id = '';

CREATE INDEX IF NOT EXISTS objects_version_idx ON objects (tenant_id, bucket, key, updated_at DESC);

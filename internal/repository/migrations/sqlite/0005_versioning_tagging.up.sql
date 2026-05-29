-- Per-bucket versioning toggle. NULL = off (legacy buckets default to off).
ALTER TABLE buckets ADD COLUMN versioning INTEGER NOT NULL DEFAULT 0;

-- Per-bucket object lock retention (seconds). 0 = no retention.
ALTER TABLE buckets ADD COLUMN object_lock_seconds INTEGER NOT NULL DEFAULT 0;

-- Per-object version_id. Existing rows get a generated UUID-ish value so
-- queries that filter by version remain deterministic.
ALTER TABLE objects ADD COLUMN version_id TEXT NOT NULL DEFAULT '';
ALTER TABLE objects ADD COLUMN locked_until TEXT;
UPDATE objects SET version_id = printf('%016x-%08x', id, abs(random()) % 4294967295) WHERE version_id = '';

CREATE INDEX objects_tagged_idx ON objects (tenant_id, bucket, key);
CREATE INDEX objects_version_idx ON objects (tenant_id, bucket, key, updated_at DESC);

-- Add per-bucket storage quota (optional, 0 = unlimited).
-- bucket_max_bytes: max total bytes (0 = unlimited)
-- bucket_max_objects: max object count (0 = unlimited)
ALTER TABLE buckets ADD COLUMN bucket_max_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE buckets ADD COLUMN bucket_max_objects INTEGER NOT NULL DEFAULT 0;

-- Add per-bucket SSE (server-side encryption) configuration.
ALTER TABLE buckets ADD COLUMN sse_algorithm TEXT NOT NULL DEFAULT '';
ALTER TABLE buckets ADD COLUMN sse_kms_key_id TEXT NOT NULL DEFAULT '';

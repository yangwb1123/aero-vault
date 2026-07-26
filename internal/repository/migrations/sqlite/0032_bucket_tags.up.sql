-- Add bucket-level tags (JSON map, like object tags but for buckets).
ALTER TABLE buckets ADD COLUMN tags TEXT NOT NULL DEFAULT '';

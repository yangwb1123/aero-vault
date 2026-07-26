-- Add per-bucket website hosting configuration.
ALTER TABLE buckets ADD COLUMN website_config TEXT NOT NULL DEFAULT '';

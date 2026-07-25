-- Add noncurrent version retention fields for lifecycle management.
ALTER TABLE buckets ADD COLUMN noncurrent_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE buckets ADD COLUMN noncurrent_count INTEGER NOT NULL DEFAULT 0;

-- Add noncurrent version retention fields for lifecycle management.
-- NoncurrentDays: days after which non-current versions expire (0 = disabled)
-- NoncurrentCount: max non-current versions to retain (0 = unlimited)
ALTER TABLE buckets ADD COLUMN noncurrent_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE buckets ADD COLUMN noncurrent_count INTEGER NOT NULL DEFAULT 0;

-- Add transition rules for lifecycle management.
ALTER TABLE buckets ADD COLUMN transition_rules TEXT NOT NULL DEFAULT '';
ALTER TABLE buckets ADD COLUMN noncurrent_transition_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE buckets ADD COLUMN noncurrent_transition_storage_class TEXT NOT NULL DEFAULT '';

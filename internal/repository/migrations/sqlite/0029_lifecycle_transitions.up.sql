-- Add transition rules for lifecycle management.
-- transition_rules: JSON array of {"days":N,"storage_class":"..."} entries
-- noncurrent_transition_days / noncurrent_transition_storage_class: transition non-current versions
ALTER TABLE buckets ADD COLUMN transition_rules TEXT NOT NULL DEFAULT '';
ALTER TABLE buckets ADD COLUMN noncurrent_transition_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE buckets ADD COLUMN noncurrent_transition_storage_class TEXT NOT NULL DEFAULT '';

ALTER TABLE buckets ADD COLUMN expire_after_days INTEGER NOT NULL DEFAULT 0;
ALTER TABLE buckets ADD COLUMN expire_action TEXT NOT NULL DEFAULT 'soft_delete';

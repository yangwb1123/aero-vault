-- Add version_tombstone flag to distinguish versioning tombstones (set by
-- InsertObjectVersion) from user-initiated soft-deletes (set by SoftDeleteObject).
-- The retention GC must NOT delete version tombstones — they are handled by
-- the lifecycle sweep (NoncurrentDays / NoncurrentCount).
ALTER TABLE objects ADD COLUMN version_tombstone INTEGER NOT NULL DEFAULT 0;

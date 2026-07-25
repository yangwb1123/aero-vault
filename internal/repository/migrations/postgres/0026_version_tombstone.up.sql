-- Add version_tombstone flag to distinguish versioning tombstones from
-- user-initiated soft-deletes.
ALTER TABLE objects ADD COLUMN version_tombstone BOOLEAN NOT NULL DEFAULT false;

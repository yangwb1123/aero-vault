ALTER TABLE multipart_uploads ADD COLUMN content_type TEXT NOT NULL DEFAULT '';
ALTER TABLE multipart_uploads ADD COLUMN storage_class TEXT NOT NULL DEFAULT '';
ALTER TABLE multipart_uploads ADD COLUMN tags JSONB NOT NULL DEFAULT '{}'::jsonb;

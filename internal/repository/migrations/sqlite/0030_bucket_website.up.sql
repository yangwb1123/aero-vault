-- Add per-bucket website hosting configuration.
-- website_config: JSON object with IndexDocument.Suffix and ErrorDocument.Key
ALTER TABLE buckets ADD COLUMN website_config TEXT NOT NULL DEFAULT '';

-- Add per-bucket SSE (server-side encryption) configuration.
-- SSEAlgorithm: "" (none/inherit), "AES256", or "aws:kms"
-- SSEKMSKeyID: KMS key ARN/alias (only meaningful when SSEAlgorithm = "aws:kms")
ALTER TABLE buckets ADD COLUMN sse_algorithm TEXT NOT NULL DEFAULT '';
ALTER TABLE buckets ADD COLUMN sse_kms_key_id TEXT NOT NULL DEFAULT '';

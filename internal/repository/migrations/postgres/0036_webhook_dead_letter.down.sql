DROP INDEX IF EXISTS webhook_failures_retryable_idx;
ALTER TABLE webhook_failures DROP COLUMN dead_lettered;

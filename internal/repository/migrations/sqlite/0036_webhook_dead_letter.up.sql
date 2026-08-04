ALTER TABLE webhook_failures ADD COLUMN dead_lettered INTEGER NOT NULL DEFAULT 0;
CREATE INDEX webhook_failures_retryable_idx
  ON webhook_failures (succeeded, dead_lettered, next_retry_at);

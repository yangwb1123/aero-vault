DELETE FROM billing_usage_outbox WHERE status='failed';
ALTER TABLE billing_usage_outbox
  DROP CONSTRAINT IF EXISTS billing_usage_outbox_status_check;
ALTER TABLE billing_usage_outbox
  ADD CONSTRAINT billing_usage_outbox_status_check
  CHECK (status IN ('pending', 'inflight', 'delivered'));

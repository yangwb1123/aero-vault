-- Rollback (manual only, I2): anchors are dropped and behavior returns to
-- retry-forever (pre-cumulative-window). Terminal rows already failed remain
-- failed (0042 failed_at_ns untouched).
ALTER TABLE audit_governance_outbox
  DROP COLUMN first_attempt_at_ns;

ALTER TABLE audit_governance_outbox
  ADD COLUMN failed_at_ns BIGINT NOT NULL DEFAULT 0;

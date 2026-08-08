-- Terminal-with-retention state for audit_governance_outbox: a receipt with
-- conflict:true is a permanent semantic conflict (retry cannot succeed), so
-- the relay marks the row failed_at_ns instead of rescheduling it forever.
-- Failed rows are excluded from claim (failed_at_ns=0) and pruned by
-- CleanupFailedAuditGovernance after the delivered-retention window (7d
-- default), mirroring the events outbox delivered/failed prune split.
ALTER TABLE audit_governance_outbox
  ADD COLUMN failed_at_ns INTEGER NOT NULL DEFAULT 0;

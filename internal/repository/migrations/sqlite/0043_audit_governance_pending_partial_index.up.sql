-- Pending-row partial indexes for the audit-governance relay's two pending
-- access paths (claim + lag), carrying the exact predicate claim/lag/fail
-- already filter by: WHERE delivered_at_ns = 0 AND failed_at_ns = 0.
--
-- Deviation note (contract implementation-gate.md:21 item 1): the contract
-- prescribes status/dead_at columns + partial index; 0042 shipped
-- failed_at_ns (timestamp-led 0039 schema — claim/lag already exclude
-- failed_at_ns != 0, CleanupFailedAuditGovernance prunes by failed_at_ns +
-- retention). Deviation documented, not renamed (zero-behavior rename; I2).
--
-- Claim path: WHERE delivered_at_ns=0 AND failed_at_ns=0 AND available_at_ns<=?
--   AND lease_expires_at_ns<=? ... ORDER BY available_at_ns,created_at_ns,id —
--   (available_at_ns, created_at_ns, id) serves the range predicate AND the
--   ORDER BY in index order (no temp sort, EXPLAIN-verified on SQLite and
--   Postgres); lease_expires_at_ns stays a residual filter.
-- Lag path: MIN(created_at_ns) in OldestPendingAuditGovernance — served by
--   (created_at_ns). (The EXISTS in HasPendingDrainingAuditGovernance
--   resolves via the pre-existing tenant_idx on SQLite and via this index on
--   Postgres; the MIN probe is the binding assertion in both dialects.)
CREATE INDEX audit_governance_pending_claim_idx ON audit_governance_outbox
  (available_at_ns, created_at_ns, id)
  WHERE delivered_at_ns = 0 AND failed_at_ns = 0;
CREATE INDEX audit_governance_pending_lag_idx ON audit_governance_outbox
  (created_at_ns)
  WHERE delivered_at_ns = 0 AND failed_at_ns = 0;

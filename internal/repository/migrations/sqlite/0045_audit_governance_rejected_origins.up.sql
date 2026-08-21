-- Permanent delivery rejection tombstones.  A failed row may be pruned, but
-- its immutable origin must remain excluded from gap reconciliation so a
-- permanent receiver rejection is never resurrected.
CREATE TABLE audit_governance_rejected_origins (
  origin_kind   TEXT NOT NULL CHECK (origin_kind IN ('admin', 'file')),
  origin_id     INTEGER NOT NULL,
  rejected_at_ns INTEGER NOT NULL,
  PRIMARY KEY (origin_kind, origin_id)
);

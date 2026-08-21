CREATE TABLE audit_governance_rejected_origins (
  origin_kind    TEXT NOT NULL CHECK (origin_kind IN ('admin', 'file')),
  origin_id      BIGINT NOT NULL,
  rejected_at_ns BIGINT NOT NULL,
  PRIMARY KEY (origin_kind, origin_id)
);

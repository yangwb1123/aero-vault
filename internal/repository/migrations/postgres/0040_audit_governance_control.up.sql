CREATE TABLE audit_governance_control (
  singleton       BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  revision        BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
  desired_digest  TEXT NOT NULL DEFAULT '',
  updated_at_ns   BIGINT NOT NULL DEFAULT 0
);

INSERT INTO audit_governance_control (singleton) VALUES (TRUE);

CREATE TABLE audit_governance_bindings (
  tenant_id      TEXT PRIMARY KEY,
  state          TEXT NOT NULL CHECK (state IN ('active', 'draining')),
  revision       BIGINT NOT NULL CHECK (revision > 0),
  updated_at_ns  BIGINT NOT NULL
);

CREATE INDEX audit_governance_binding_state_idx
  ON audit_governance_bindings (state, tenant_id);

CREATE TABLE audit_governance_delivered_origins (
  origin_kind      TEXT NOT NULL CHECK (origin_kind IN ('admin', 'file')),
  origin_id        BIGINT NOT NULL,
  delivered_at_ns  BIGINT NOT NULL,
  PRIMARY KEY (origin_kind, origin_id)
);

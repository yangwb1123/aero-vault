CREATE TABLE audit_governance_control (
  singleton       INTEGER PRIMARY KEY CHECK (singleton = 1),
  revision        INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
  desired_digest  TEXT NOT NULL DEFAULT '',
  updated_at_ns   INTEGER NOT NULL DEFAULT 0
);

INSERT INTO audit_governance_control (singleton) VALUES (1);

CREATE TABLE audit_governance_bindings (
  tenant_id      TEXT PRIMARY KEY,
  state          TEXT NOT NULL CHECK (state IN ('active', 'draining')),
  revision       INTEGER NOT NULL CHECK (revision > 0),
  updated_at_ns  INTEGER NOT NULL
);

CREATE INDEX audit_governance_binding_state_idx
  ON audit_governance_bindings (state, tenant_id);

CREATE TABLE audit_governance_delivered_origins (
  origin_kind      TEXT NOT NULL CHECK (origin_kind IN ('admin', 'file')),
  origin_id        INTEGER NOT NULL,
  delivered_at_ns  INTEGER NOT NULL,
  PRIMARY KEY (origin_kind, origin_id)
);

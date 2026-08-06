CREATE TABLE audit_governance_outbox (
  id                    TEXT PRIMARY KEY,
  tenant_id             TEXT NOT NULL,
  origin_kind           TEXT NOT NULL CHECK (origin_kind IN ('admin', 'file')),
  origin_id             INTEGER NOT NULL,
  fact_kind             TEXT NOT NULL CHECK (fact_kind IN ('admin', 'security', 'file')),
  actor_digest          TEXT NOT NULL DEFAULT '',
  action                TEXT NOT NULL,
  target_digest         TEXT NOT NULL DEFAULT '',
  request_id            TEXT NOT NULL DEFAULT '',
  detail_sha256         TEXT NOT NULL DEFAULT '',
  object_size_bytes     INTEGER NOT NULL DEFAULT 0 CHECK (object_size_bytes >= 0),
  storage_backend       TEXT NOT NULL DEFAULT '',
  occurred_at_ns        INTEGER NOT NULL,
  attempts              INTEGER NOT NULL DEFAULT 0,
  available_at_ns       INTEGER NOT NULL,
  claim_owner           TEXT NOT NULL DEFAULT '',
  claim_token           TEXT NOT NULL DEFAULT '',
  lease_expires_at_ns   INTEGER NOT NULL DEFAULT 0,
  last_error            TEXT NOT NULL DEFAULT '',
  created_at_ns         INTEGER NOT NULL,
  delivered_at_ns       INTEGER NOT NULL DEFAULT 0,
  UNIQUE (origin_kind, origin_id)
);

CREATE INDEX audit_governance_due_idx
  ON audit_governance_outbox
  (delivered_at_ns, available_at_ns, lease_expires_at_ns, created_at_ns);
CREATE INDEX audit_governance_tenant_idx
  ON audit_governance_outbox (tenant_id, created_at_ns);

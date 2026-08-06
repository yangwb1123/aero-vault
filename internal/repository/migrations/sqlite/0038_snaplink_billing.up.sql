CREATE TABLE billing_entitlements (
  tenant_id          TEXT PRIMARY KEY,
  revision           INTEGER NOT NULL,
  active             INTEGER NOT NULL CHECK (active IN (0, 1)),
  bytes_hard         INTEGER NOT NULL CHECK (bytes_hard >= 0),
  bytes_unlimited    INTEGER NOT NULL CHECK (bytes_unlimited IN (0, 1)),
  objects_hard       INTEGER NOT NULL CHECK (objects_hard >= 0),
  objects_unlimited  INTEGER NOT NULL CHECK (objects_unlimited IN (0, 1)),
  effective_at_ns    INTEGER NOT NULL,
  expires_at_ns      INTEGER NOT NULL DEFAULT 0,
  projected_at_ns    INTEGER NOT NULL
);

CREATE TABLE billing_usage_operations (
  operation_id       TEXT PRIMARY KEY,
  tenant_id          TEXT NOT NULL,
  delta_bytes        INTEGER NOT NULL,
  delta_objects      INTEGER NOT NULL,
  kind               TEXT NOT NULL,
  occurred_at_ns     INTEGER NOT NULL,
  created_at_ns      INTEGER NOT NULL
);

CREATE TABLE billing_usage_outbox (
  id                 TEXT PRIMARY KEY,
  operation_id       TEXT NOT NULL REFERENCES billing_usage_operations(operation_id) ON DELETE CASCADE,
  tenant_id          TEXT NOT NULL,
  dimension          TEXT NOT NULL,
  quantity           INTEGER NOT NULL CHECK (quantity > 0),
  occurred_at_ns     INTEGER NOT NULL,
  metadata_json      TEXT NOT NULL DEFAULT '{}',
  status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'inflight', 'delivered')),
  attempts           INTEGER NOT NULL DEFAULT 0,
  next_attempt_at_ns INTEGER NOT NULL,
  claim_owner        TEXT NOT NULL DEFAULT '',
  claim_until_ns     INTEGER NOT NULL DEFAULT 0,
  last_error         TEXT NOT NULL DEFAULT '',
  created_at_ns      INTEGER NOT NULL,
  delivered_at_ns    INTEGER NOT NULL DEFAULT 0,
  UNIQUE (operation_id, dimension)
);

CREATE INDEX billing_usage_due_idx
  ON billing_usage_outbox (status, next_attempt_at_ns, claim_until_ns, created_at_ns);
CREATE INDEX billing_usage_tenant_idx
  ON billing_usage_outbox (tenant_id, created_at_ns);

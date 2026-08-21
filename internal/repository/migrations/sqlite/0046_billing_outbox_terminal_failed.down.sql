-- Manual rollback only: terminal rows cannot satisfy the old CHECK and are
-- intentionally discarded before restoring the three-value constraint.
CREATE TABLE billing_usage_outbox_old (
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
INSERT INTO billing_usage_outbox_old
  (id, operation_id, tenant_id, dimension, quantity, occurred_at_ns, metadata_json,
   status, attempts, next_attempt_at_ns, claim_owner, claim_until_ns, last_error,
   created_at_ns, delivered_at_ns)
SELECT id, operation_id, tenant_id, dimension, quantity, occurred_at_ns, metadata_json,
  status, attempts, next_attempt_at_ns, claim_owner, claim_until_ns, last_error,
  created_at_ns, delivered_at_ns
FROM billing_usage_outbox
WHERE status <> 'failed';
DROP TABLE billing_usage_outbox;
ALTER TABLE billing_usage_outbox_old RENAME TO billing_usage_outbox;
CREATE INDEX billing_usage_due_idx
  ON billing_usage_outbox (status, next_attempt_at_ns, claim_until_ns, created_at_ns);
CREATE INDEX billing_usage_tenant_idx
  ON billing_usage_outbox (tenant_id, created_at_ns);

CREATE TABLE event_outbox (
  id                  BIGSERIAL PRIMARY KEY, -- authority dedupe key; sequence never reuses a row id
  event_type          TEXT NOT NULL CHECK (event_type IN
                        ('vault.file.deleted@1.1','vault.file.notify@1.1')),
  origin_id           BIGINT NOT NULL,       -- objects.id reference; no FK (RestoreObject reuses row ids, reconcile prunes objects)
  tenant_id           TEXT NOT NULL,
  payload             TEXT NOT NULL,         -- self-contained JSON; TEXT (not jsonb) keeps bytes byte-exact
  attempts            INTEGER NOT NULL DEFAULT 0,
  status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','inflight','delivered','failed')),
  available_at_ns     BIGINT NOT NULL,
  claim_owner         TEXT NOT NULL DEFAULT '',
  claim_token         TEXT NOT NULL DEFAULT '',
  lease_expires_at_ns BIGINT NOT NULL DEFAULT 0,
  last_error          TEXT NOT NULL DEFAULT '',
  created_at_ns       BIGINT NOT NULL,
  delivered_at_ns     BIGINT NOT NULL DEFAULT 0
);

-- Status-led due index (billing_usage_due_idx shape). Cannot serve the OR
-- claim predicate on Postgres (seq-scan) — accepted at outbox scale.
CREATE INDEX event_outbox_due_idx
  ON event_outbox (status, available_at_ns, lease_expires_at_ns, created_at_ns);
CREATE INDEX event_outbox_tenant_idx
  ON event_outbox (tenant_id, created_at_ns);
-- Notifier conditional-skip query (D2): per-delete EXISTS by origin + type.
CREATE INDEX event_outbox_origin_idx
  ON event_outbox (origin_id, event_type);

-- Fidelity record keyed by the outbox row id (D1): written in the same
-- transaction as complete; functionally inert for dedupe (the claim predicate
-- already excludes delivered rows) but retained for AC-2(4) fidelity + audit.
CREATE TABLE event_outbox_delivered (
  outbox_id       BIGSERIAL PRIMARY KEY,
  delivered_at_ns BIGINT NOT NULL
);

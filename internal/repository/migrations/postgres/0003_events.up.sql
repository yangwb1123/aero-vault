CREATE TABLE IF NOT EXISTS object_events (
  id          BIGSERIAL PRIMARY KEY,
  tenant_id   TEXT NOT NULL,
  bucket      TEXT NOT NULL,
  key         TEXT NOT NULL,
  type        TEXT NOT NULL,
  object_id   BIGINT,
  request_id  TEXT NOT NULL DEFAULT '',
  payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
  consumed_at TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS object_events_unconsumed_idx
  ON object_events (consumed_at, id) WHERE consumed_at IS NULL;
CREATE INDEX IF NOT EXISTS object_events_tenant_idx
  ON object_events (tenant_id, created_at);

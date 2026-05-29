CREATE TABLE IF NOT EXISTS tenant_quotas (
  tenant_id    TEXT PRIMARY KEY,
  max_bytes    BIGINT NOT NULL DEFAULT 0,
  max_objects  BIGINT NOT NULL DEFAULT 0,
  used_bytes   BIGINT NOT NULL DEFAULT 0,
  used_objects BIGINT NOT NULL DEFAULT 0,
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

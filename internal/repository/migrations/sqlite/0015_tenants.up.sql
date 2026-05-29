CREATE TABLE tenants (
  tenant_id    TEXT PRIMARY KEY,
  display_name TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'active',   -- 'active' | 'disabled'
  created_at   TEXT NOT NULL
);

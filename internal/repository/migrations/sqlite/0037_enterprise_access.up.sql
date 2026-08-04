CREATE TABLE departments (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    parent_id TEXT,
    name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (tenant_id, parent_id, name),
    FOREIGN KEY (parent_id) REFERENCES departments(id) ON DELETE CASCADE
);

CREATE INDEX idx_departments_tenant_parent
    ON departments(tenant_id, parent_id, name);

CREATE TABLE department_members (
    tenant_id TEXT NOT NULL,
    department_id TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'member',
    created_at TEXT NOT NULL,
    PRIMARY KEY (tenant_id, department_id, subject_id),
    FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE CASCADE
);

CREATE INDEX idx_department_members_subject
    ON department_members(tenant_id, subject_id, department_id);

CREATE TABLE resource_acls (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    bucket TEXT NOT NULL,
    resource_key TEXT NOT NULL DEFAULT '',
    resource_kind TEXT NOT NULL,
    principal_type TEXT NOT NULL,
    principal_id TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    effect TEXT NOT NULL,
    inherit_acl INTEGER NOT NULL DEFAULT 0,
    created_by TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE (
        tenant_id, bucket, resource_key, resource_kind,
        principal_type, principal_id, action
    )
);

CREATE INDEX idx_resource_acls_lookup
    ON resource_acls(tenant_id, bucket, resource_key, resource_kind);

CREATE TABLE shares (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    bucket TEXT NOT NULL,
    object_key TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    token_hash TEXT NOT NULL UNIQUE,
    password_mac TEXT NOT NULL DEFAULT '',
    allow_preview INTEGER NOT NULL DEFAULT 1,
    allow_download INTEGER NOT NULL DEFAULT 0,
    max_uses INTEGER NOT NULL DEFAULT 0,
    use_count INTEGER NOT NULL DEFAULT 0,
    expires_at TEXT NOT NULL DEFAULT '',
    revoked_at TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    owner_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE INDEX idx_shares_resource
    ON shares(tenant_id, bucket, object_key, created_at);

CREATE TABLE public_assets (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    bucket TEXT NOT NULL,
    object_key TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    cache_control TEXT NOT NULL DEFAULT 'public, max-age=3600',
    published_by TEXT NOT NULL DEFAULT '',
    owner_id TEXT NOT NULL DEFAULT '',
    published_at TEXT NOT NULL
);

CREATE INDEX idx_public_assets_tenant
    ON public_assets(tenant_id, published_at);

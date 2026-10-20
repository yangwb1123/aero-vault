-- Unconnected persistence foundation. No existing buckets/objects are adopted.
CREATE TABLE spaces (
  tenant_id TEXT NOT NULL CHECK (length(tenant_id) > 0 AND tenant_id <> '*'),
  id TEXT NOT NULL CHECK (length(id) > 0),
  kind TEXT NOT NULL CHECK (kind IN ('personal','team')),
  name TEXT NOT NULL CHECK (length(name) > 0),
  backing_bucket TEXT NOT NULL CHECK (length(backing_bucket) > 0),
  personal_source TEXT,
  personal_kind TEXT,
  personal_subject_id TEXT,
  revision BIGINT NOT NULL CHECK (revision >= 1),
  created_by_source TEXT NOT NULL CHECK (length(created_by_source) > 0),
  created_by_kind TEXT NOT NULL CHECK (created_by_kind IN ('user','service')),
  created_by_id TEXT NOT NULL CHECK (length(created_by_id) > 0),
  updated_by_source TEXT NOT NULL CHECK (length(updated_by_source) > 0),
  updated_by_kind TEXT NOT NULL CHECK (updated_by_kind IN ('user','service')),
  updated_by_id TEXT NOT NULL CHECK (length(updated_by_id) > 0),
  created_at_ns BIGINT NOT NULL CHECK (created_at_ns > 0),
  updated_at_ns BIGINT NOT NULL CHECK (updated_at_ns > 0),
  CONSTRAINT spaces_pkey PRIMARY KEY (tenant_id,id),
  CONSTRAINT spaces_bucket_uq UNIQUE (tenant_id,backing_bucket),
  FOREIGN KEY (tenant_id) REFERENCES tenants(tenant_id) ON UPDATE RESTRICT ON DELETE RESTRICT,
  FOREIGN KEY (tenant_id,backing_bucket) REFERENCES buckets(tenant_id,name) ON UPDATE RESTRICT ON DELETE RESTRICT,
  CHECK ((kind = 'personal' AND personal_source IS NOT NULL AND length(personal_source) > 0
          AND personal_kind IS NOT NULL AND personal_kind = 'user'
          AND personal_subject_id IS NOT NULL AND length(personal_subject_id) > 0)
      OR (kind = 'team' AND personal_source IS NULL AND personal_kind IS NULL AND personal_subject_id IS NULL))
);
CREATE UNIQUE INDEX spaces_personal_subject_uq
  ON spaces(tenant_id,personal_source,personal_kind,personal_subject_id) WHERE kind = 'personal';

CREATE TABLE space_members (
  tenant_id TEXT NOT NULL,
  space_id TEXT NOT NULL,
  subject_source TEXT NOT NULL CHECK (length(subject_source) > 0),
  subject_kind TEXT NOT NULL CHECK (subject_kind IN ('user','service')),
  subject_id TEXT NOT NULL CHECK (length(subject_id) > 0),
  role TEXT NOT NULL CHECK (role IN ('viewer','editor','admin')),
  state TEXT NOT NULL CHECK (state IN ('active','revoked')),
  changed_revision BIGINT NOT NULL CHECK (changed_revision >= 1),
  changed_by_source TEXT NOT NULL CHECK (length(changed_by_source) > 0),
  changed_by_kind TEXT NOT NULL CHECK (changed_by_kind IN ('user','service')),
  changed_by_id TEXT NOT NULL CHECK (length(changed_by_id) > 0),
  joined_at_ns BIGINT NOT NULL CHECK (joined_at_ns > 0),
  updated_at_ns BIGINT NOT NULL CHECK (updated_at_ns > 0),
  revoked_at_ns BIGINT CHECK (revoked_at_ns IS NULL OR revoked_at_ns > 0),
  CONSTRAINT space_members_pkey PRIMARY KEY (tenant_id,space_id,subject_source,subject_kind,subject_id),
  FOREIGN KEY (tenant_id,space_id) REFERENCES spaces(tenant_id,id) ON UPDATE RESTRICT ON DELETE RESTRICT,
  CHECK ((state = 'active' AND revoked_at_ns IS NULL) OR (state = 'revoked' AND revoked_at_ns IS NOT NULL))
);
CREATE INDEX space_members_subject_idx
  ON space_members(tenant_id,subject_source,subject_kind,subject_id,state,space_id);
CREATE INDEX space_members_admin_idx
  ON space_members(tenant_id,space_id,state,role,subject_kind);

CREATE TABLE space_history (
  tenant_id TEXT NOT NULL,
  id TEXT NOT NULL CHECK (length(id) > 0),
  space_id TEXT NOT NULL,
  command_id TEXT NOT NULL CHECK (length(command_id) > 0),
  command_type TEXT NOT NULL CHECK (command_type IN ('create','member.add','member.change_role','member.revoke','member.reactivate')),
  actor_source TEXT NOT NULL CHECK (length(actor_source) > 0),
  actor_kind TEXT NOT NULL CHECK (actor_kind IN ('user','service')),
  actor_id TEXT NOT NULL CHECK (length(actor_id) > 0),
  target_source TEXT NOT NULL CHECK (length(target_source) > 0),
  target_kind TEXT NOT NULL CHECK (target_kind IN ('user','service')),
  target_id TEXT NOT NULL CHECK (length(target_id) > 0),
  before_revision BIGINT NOT NULL CHECK (before_revision >= 0),
  after_revision BIGINT NOT NULL CHECK (after_revision > 0),
  before_role TEXT CHECK (before_role IS NULL OR before_role IN ('viewer','editor','admin')),
  after_role TEXT NOT NULL CHECK (after_role IN ('viewer','editor','admin')),
  before_state TEXT CHECK (before_state IS NULL OR before_state IN ('active','revoked')),
  after_state TEXT NOT NULL CHECK (after_state IN ('active','revoked')),
  reason TEXT NOT NULL,
  request_id TEXT NOT NULL,
  occurred_at_ns BIGINT NOT NULL CHECK (occurred_at_ns > 0),
  CONSTRAINT space_history_pkey PRIMARY KEY (tenant_id,id),
  CONSTRAINT space_history_command_uq UNIQUE (tenant_id,command_id),
  CONSTRAINT space_history_revision_uq UNIQUE (tenant_id,space_id,after_revision),
  FOREIGN KEY (tenant_id,space_id) REFERENCES spaces(tenant_id,id) ON UPDATE RESTRICT ON DELETE RESTRICT,
  CHECK (after_revision = before_revision + 1),
  CHECK ((command_type = 'create' AND before_revision = 0 AND after_revision = 1)
      OR (command_type <> 'create' AND before_revision >= 1)),
  CHECK ((command_type IN ('create','member.add') AND before_role IS NULL AND before_state IS NULL AND after_state = 'active')
      OR (command_type = 'member.change_role' AND before_role IS NOT NULL AND before_state IS NOT NULL
          AND before_state = 'active' AND after_state = 'active' AND before_role <> after_role)
      OR (command_type = 'member.revoke' AND before_role IS NOT NULL AND before_state IS NOT NULL
          AND before_state = 'active' AND after_state = 'revoked' AND before_role = after_role)
      OR (command_type = 'member.reactivate' AND before_role IS NOT NULL AND before_state IS NOT NULL
          AND before_state = 'revoked' AND after_state = 'active'))
);

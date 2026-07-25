-- Legal hold is a first-class compliance entity, no longer a metadata tag.
-- A legal hold prevents object deletion (including lifecycle and retention GC)
-- for as long as the hold is active. Holds are per-version or apply to all
-- versions (version_id = NULL).
CREATE TABLE IF NOT EXISTS legal_holds (
    object_id   BIGINT       NOT NULL,
    tenant_id   TEXT         NOT NULL,
    version_id  TEXT,               -- NULL = applies to all versions of the object
    hold_reason TEXT         NOT NULL DEFAULT '',
    created_by  TEXT         NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (object_id, version_id),
    FOREIGN KEY (object_id) REFERENCES objects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS legal_holds_tenant_idx ON legal_holds (tenant_id, object_id);

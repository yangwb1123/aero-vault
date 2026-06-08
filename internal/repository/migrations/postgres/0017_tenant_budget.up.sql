ALTER TABLE tenant_quotas ADD COLUMN daily_budget_micros BIGINT NOT NULL DEFAULT 0;

ALTER TABLE ai_usage DROP COLUMN cost_micros;
ALTER TABLE ai_usage DROP COLUMN latency_ms;
ALTER TABLE ai_usage DROP COLUMN total_tokens;
ALTER TABLE ai_usage DROP COLUMN completion_tokens;
ALTER TABLE ai_usage DROP COLUMN prompt_tokens;
ALTER TABLE ai_usage DROP COLUMN model;

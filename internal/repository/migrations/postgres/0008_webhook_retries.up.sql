CREATE TABLE IF NOT EXISTS webhook_failures (
  id            BIGSERIAL PRIMARY KEY,
  event_id      BIGINT NOT NULL,
  url           TEXT NOT NULL,
  payload       TEXT NOT NULL,
  attempts      INT  NOT NULL DEFAULT 1,
  last_error    TEXT NOT NULL DEFAULT '',
  last_status   INT  NOT NULL DEFAULT 0,
  next_retry_at TIMESTAMPTZ NOT NULL,
  succeeded     BOOLEAN NOT NULL DEFAULT false,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS webhook_failures_pending_idx ON webhook_failures (succeeded, next_retry_at);

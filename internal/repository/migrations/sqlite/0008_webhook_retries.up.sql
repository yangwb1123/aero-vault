CREATE TABLE webhook_failures (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id      INTEGER NOT NULL,
  url           TEXT NOT NULL,
  payload       TEXT NOT NULL,
  attempts      INTEGER NOT NULL DEFAULT 1,
  last_error    TEXT NOT NULL DEFAULT '',
  last_status   INTEGER NOT NULL DEFAULT 0,
  next_retry_at TEXT NOT NULL,
  succeeded     INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX webhook_failures_pending_idx ON webhook_failures (succeeded, next_retry_at);

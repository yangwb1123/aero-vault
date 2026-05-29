CREATE TABLE leases (
  name       TEXT PRIMARY KEY,            -- logical singleton name, e.g. 'reconcile-sweep'
  holder     TEXT NOT NULL,               -- opaque instance id of the current holder
  expires_at TEXT NOT NULL                -- RFC3339Nano; lease is free once now > expires_at
);

-- +goose Up
CREATE TABLE instances (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  controller_url TEXT NOT NULL,
  secret_ciphertext BLOB NOT NULL,
  enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
  collect_enabled INTEGER NOT NULL CHECK (collect_enabled IN (0,1)),
  created_at_ms INTEGER NOT NULL,
  updated_at_ms INTEGER NOT NULL,
  deleted_at_ms INTEGER
);
CREATE TABLE admin (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  username TEXT NOT NULL,
  password_hash TEXT NOT NULL
);
CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  target_id TEXT,
  result TEXT NOT NULL,
  at_ms INTEGER NOT NULL
);
CREATE TABLE readiness_probe (id INTEGER PRIMARY KEY CHECK (id = 1), value INTEGER NOT NULL);
INSERT INTO readiness_probe VALUES (1, 0);

-- +goose Down
DROP TABLE readiness_probe;
DROP TABLE audit_events;
DROP TABLE admin;
DROP TABLE instances;

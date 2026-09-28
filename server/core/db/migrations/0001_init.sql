-- Phase 1: core schema. Users, device registry, audit log, agent command queue.

CREATE TABLE IF NOT EXISTS users (
  id            TEXT PRIMARY KEY,
  username      TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL,
  role          TEXT NOT NULL DEFAULT 'viewer',   -- admin|technician|viewer
  created_at    DATETIME NOT NULL,
  updated_at    DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
  id                    TEXT PRIMARY KEY,
  hostname              TEXT NOT NULL,
  os_name               TEXT NOT NULL,             -- windows|linux|macos
  os_version            TEXT,
  agent_version         TEXT,
  status                TEXT NOT NULL DEFAULT 'offline', -- online|offline
  last_seen_at          DATETIME,
  enrolled_at           DATETIME NOT NULL,
  enrollment_token_hash TEXT,                     -- one-time token (hashed)
  device_secret_hash    TEXT NOT NULL,            -- persistent secret (hashed)
  site                  TEXT,                     -- kantor pusat / cabang-xx
  created_at            DATETIME NOT NULL,
  updated_at            DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_devices_status    ON devices(status);
CREATE INDEX IF NOT EXISTS idx_devices_last_seen ON devices(last_seen_at);

CREATE TABLE IF NOT EXISTS audit_logs (
  id         TEXT PRIMARY KEY,
  actor_type TEXT NOT NULL,                       -- user|agent|system
  actor_id   TEXT,
  action     TEXT NOT NULL,                       -- device.enroll, auth.login, ...
  target_id  TEXT,
  details    TEXT,                                -- JSON
  created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at);

CREATE TABLE IF NOT EXISTS agent_commands (
  id           TEXT PRIMARY KEY,
  device_id    TEXT NOT NULL,
  command_type TEXT NOT NULL,                     -- ping|shell|inventory.collect|...
  payload      TEXT,                              -- JSON
  status       TEXT NOT NULL DEFAULT 'pending',   -- pending|sent|done|failed
  created_at   DATETIME NOT NULL,
  sent_at      DATETIME,
  completed_at DATETIME,
  result       TEXT
);
-- Critical index for scale: "pending commands for device X" must be fast.
CREATE INDEX IF NOT EXISTS idx_commands_device_status ON agent_commands(device_id, status);
CREATE INDEX IF NOT EXISTS idx_commands_created ON agent_commands(created_at);

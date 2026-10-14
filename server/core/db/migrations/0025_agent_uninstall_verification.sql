-- Migration 0025: Agent Uninstall Verification Tokens
CREATE TABLE IF NOT EXISTS device_uninstall_tokens (
    id TEXT PRIMARY KEY,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL,
    used_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_device_uninstall_tokens_device ON device_uninstall_tokens(device_id);

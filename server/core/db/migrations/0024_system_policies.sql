-- Migration 0024: System Policies (Active Directory GPO-like endpoint management)
CREATE TABLE IF NOT EXISTS system_policies (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    target_type TEXT NOT NULL, -- 'all', 'group', 'device'
    target_id TEXT NOT NULL DEFAULT '',
    is_enabled INTEGER NOT NULL DEFAULT 1,
    priority INTEGER NOT NULL DEFAULT 100,
    settings_json TEXT NOT NULL DEFAULT '{}',
    created_by TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS device_system_policy_states (
    device_id TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    policy_version TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'synced', -- 'synced', 'pending', 'failed'
    applied_settings_json TEXT NOT NULL DEFAULT '{}',
    last_applied_at DATETIME NOT NULL,
    error_message TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_system_policies_target ON system_policies(target_type, target_id);

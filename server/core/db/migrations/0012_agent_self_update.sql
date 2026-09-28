-- server/core/db/migrations/0012_agent_self_update.sql
-- Phase 13: Agent Self-Update & Rollout Management

CREATE TABLE IF NOT EXISTS agent_releases (
    id TEXT PRIMARY KEY,
    version TEXT NOT NULL,
    os_name TEXT NOT NULL,
    arch TEXT NOT NULL,
    file_path TEXT NOT NULL,
    file_size INTEGER NOT NULL,
    sha256_checksum TEXT NOT NULL,
    changelog TEXT NOT NULL DEFAULT '',
    is_active INTEGER NOT NULL DEFAULT 1,
    uploaded_by TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    UNIQUE(version, os_name, arch)
);

CREATE TABLE IF NOT EXISTS update_campaigns (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    target_version TEXT NOT NULL,
    target_type TEXT NOT NULL, -- 'all', 'group', 'device'
    target_id TEXT NOT NULL DEFAULT '',
    batch_size INTEGER NOT NULL DEFAULT 20,
    stagger_interval_sec INTEGER NOT NULL DEFAULT 30,
    status TEXT NOT NULL DEFAULT 'draft', -- 'draft', 'in_progress', 'completed', 'cancelled'
    created_by TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS device_update_tasks (
    id TEXT PRIMARY KEY,
    campaign_id TEXT REFERENCES update_campaigns(id) ON DELETE SET NULL,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    from_version TEXT NOT NULL,
    target_version TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending', -- 'pending', 'downloading', 'verifying', 'swapping', 'success', 'rollback', 'failed'
    error_message TEXT NOT NULL DEFAULT '',
    dispatched_at DATETIME,
    completed_at DATETIME,
    created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_agent_releases_lookup ON agent_releases(os_name, arch, is_active);
CREATE INDEX IF NOT EXISTS idx_device_update_tasks_dev ON device_update_tasks(device_id);

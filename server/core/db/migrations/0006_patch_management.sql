-- 0006_patch_management.sql
-- Phase 6: Patch Management & OS Updates

CREATE TABLE IF NOT EXISTS device_patches (
    id TEXT PRIMARY KEY,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    patch_id TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT,
    severity TEXT NOT NULL DEFAULT 'unspecified',
    category TEXT NOT NULL DEFAULT 'security',
    kb_id TEXT,
    size_bytes INTEGER DEFAULT 0,
    installed_state TEXT NOT NULL DEFAULT 'missing',
    reboot_required INTEGER NOT NULL DEFAULT 0,
    discovered_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE(device_id, patch_id)
);

CREATE INDEX IF NOT EXISTS idx_patches_device ON device_patches(device_id);
CREATE INDEX IF NOT EXISTS idx_patches_severity ON device_patches(severity);
CREATE INDEX IF NOT EXISTS idx_patches_state ON device_patches(installed_state);
CREATE INDEX IF NOT EXISTS idx_patches_patchid ON device_patches(patch_id);

CREATE TABLE IF NOT EXISTS patch_install_jobs (
    id TEXT PRIMARY KEY,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    operator_id TEXT NOT NULL REFERENCES users(id),
    patch_ids TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    reboot_policy TEXT NOT NULL DEFAULT 'no_reboot',
    reboot_required INTEGER NOT NULL DEFAULT 0,
    output_log TEXT,
    error_message TEXT,
    started_at DATETIME NOT NULL,
    completed_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_patch_jobs_device ON patch_install_jobs(device_id);
CREATE INDEX IF NOT EXISTS idx_patch_jobs_operator ON patch_install_jobs(operator_id);
CREATE INDEX IF NOT EXISTS idx_patch_jobs_status ON patch_install_jobs(status);

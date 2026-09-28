-- server/core/db/migrations/0014_maintenance.sql
-- Phase 15: device maintenance (cleanup, disk check, memory hygiene, full scan)

CREATE TABLE IF NOT EXISTS maintenance_jobs (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL DEFAULT '',
    task_type      TEXT NOT NULL,       -- cleanup_temp, disk_check, memory_hygiene, full_scan
    target_type    TEXT NOT NULL,       -- 'device', 'group', 'all'
    target_id      TEXT NOT NULL DEFAULT '',
    created_by     TEXT NOT NULL,
    total_tasks    INTEGER NOT NULL DEFAULT 0,
    dispatched     INTEGER NOT NULL DEFAULT 0,
    skipped        INTEGER NOT NULL DEFAULT 0,  -- target devices that were offline
    completed      INTEGER NOT NULL DEFAULT 0,
    failed         INTEGER NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'running', -- 'running', 'completed', 'failed', 'partial'
    started_at     DATETIME NOT NULL,
    completed_at   DATETIME
);

CREATE TABLE IF NOT EXISTS maintenance_tasks (
    id             TEXT PRIMARY KEY,
    job_id         TEXT NOT NULL REFERENCES maintenance_jobs(id) ON DELETE CASCADE,
    device_id      TEXT NOT NULL,
    hostname       TEXT NOT NULL DEFAULT '',
    task_type      TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending', -- pending, dispatched, running, completed, failed, skipped
    -- A step is one OS action within a task (full_scan runs several). Each is
    -- kept separately so the console can show which step is running rather than
    -- one opaque "in progress".
    step           TEXT NOT NULL DEFAULT '',
    exit_code      INTEGER,
    output_log     TEXT,
    error_message  TEXT,
    reboot_required INTEGER NOT NULL DEFAULT 0,
    bytes_freed    INTEGER NOT NULL DEFAULT 0,
    started_at     DATETIME,
    completed_at   DATETIME,
    created_at     DATETIME NOT NULL,
    updated_at     DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_maint_jobs_started ON maintenance_jobs(started_at);
CREATE INDEX IF NOT EXISTS idx_maint_tasks_job ON maintenance_tasks(job_id);
CREATE INDEX IF NOT EXISTS idx_maint_tasks_device ON maintenance_tasks(device_id);
CREATE INDEX IF NOT EXISTS idx_maint_tasks_status ON maintenance_tasks(status);

-- Workflow state the background loops need to tell "waiting" from "lost".
-- Every change is additive: new nullable columns, new indexes, one data fix.

-- Agent update campaigns. Every task is created when the campaign starts;
-- released_at marks the ones a wave has let go. A pending task with no
-- released_at is waiting for its wave, one with released_at is waiting for its
-- device. progress_at is the last report the agent sent, so a download that is
-- still reporting is not reaped as stuck.
ALTER TABLE device_update_tasks ADD COLUMN released_at DATETIME;
ALTER TABLE device_update_tasks ADD COLUMN progress_at DATETIME;
-- Tasks that existed before this migration were all sent or queued already.
UPDATE device_update_tasks SET released_at = COALESCE(dispatched_at, created_at) WHERE released_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_device_update_tasks_campaign ON device_update_tasks(campaign_id, status);

-- When the next wave of an in-progress campaign is due. NULL means no wave is
-- scheduled: the campaign was started before waves were driven by the sweep.
ALTER TABLE update_campaigns ADD COLUMN next_wave_at DATETIME;

-- Why a scheduled run failed without any device run to explain it (an empty
-- group, a target that no longer exists).
ALTER TABLE scheduled_task_runs ADD COLUMN error_message TEXT;

-- Patch jobs: the sweep reaps a job by its age since dispatch.
ALTER TABLE patch_install_jobs ADD COLUMN updated_at DATETIME;

-- The measured value behind an incident (disk free %, minutes offline, patch
-- count), so the console can word it in the reader's language.
ALTER TABLE alert_incidents ADD COLUMN value REAL;
CREATE INDEX IF NOT EXISTS idx_alert_incidents_status_last ON alert_incidents(status, last_triggered_at);

-- Campaign logs are read per device for the campaign's lifetime.
CREATE INDEX IF NOT EXISTS idx_audit_target_created ON audit_logs(target_id, created_at);

-- A device_offline rule with no threshold fired the moment a device missed one
-- heartbeat, including laptops that were simply asleep. 15 minutes is the
-- default chosen for rules that never set one.
UPDATE alert_rules SET threshold_val = 15 WHERE rule_type = 'device_offline' AND threshold_val <= 0;

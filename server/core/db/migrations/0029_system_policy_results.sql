-- What the agent did with each setting of the last policy it applied:
-- [{"setting":"usb_storage_mode","outcome":"applied|failed|unsupported","reason":"..."}].
-- A device used to report one status for the whole policy, and the server read
-- anything it did not recognise as "synced", so a Linux or macOS endpoint that
-- skipped every Windows-only setting looked fully enforced. Additive: agents
-- that send no results leave the default.
ALTER TABLE device_system_policy_states ADD COLUMN results_json TEXT NOT NULL DEFAULT '[]';

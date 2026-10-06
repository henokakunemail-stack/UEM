-- 0020_patch_scan_timestamp.sql
--
-- When did this device last run a patch scan? The console has to be able to say
-- so, because "no pending updates" is only true as of a moment in time. A device
-- that has never been scanned has no pending updates for the same reason a
-- patched one does not, and without this column the two render identically.
--
-- One column, deliberately not a separate scans table: the value is only ever
-- read as "most recent", and device_patches.discovered_at cannot stand in for it
-- because it is NULL on a device whose only rows are already installed.

ALTER TABLE devices ADD COLUMN last_patch_scan_at DATETIME;

-- Existing devices get NULL. That reads as "never scanned" rather than as the
-- day the migration ran, which would date every row in the fleet to today and be
-- a worse lie than the gap it fills.

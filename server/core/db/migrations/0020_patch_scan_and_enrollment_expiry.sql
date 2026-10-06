-- 0020_patch_scan_and_enrollment_expiry.sql
--
-- Two columns on devices, both about the console telling the truth about time.

-- When did this device last run a patch scan? The console has to be able to say
-- so, because "no pending updates" is only true as of a moment in time. A device
-- that has never been scanned has no pending updates for the same reason a
-- patched one does not, and without this column the two render identically.
--
-- One column, deliberately not a separate scans table: the value is only ever
-- read as "most recent", and device_patches.discovered_at cannot stand in for it
-- because it is NULL on a device whose only rows are already installed.
ALTER TABLE devices ADD COLUMN last_patch_scan_at DATETIME;

-- When does the pending enrollment token stop working? The endpoint already
-- returned an expires_at derived from ENROLLMENT_TTL, and nothing read it:
-- ConsumeEnrollmentToken cleared only the hash, so an unused token stayed valid
-- forever. A token that never expires is a permanent credential, and enrollment
-- tokens are the kind that get pasted into a chat or left in a screenshot.
--
-- NULL means no expiry, and that is what every existing row gets. Those tokens
-- were minted by a server that never enforced a deadline, so refusing them now
-- would strand a device someone enrolled deliberately; the hash is what makes
-- them safe to keep, and it is cleared the moment they are used.
ALTER TABLE devices ADD COLUMN enrollment_token_expires_at DATETIME;

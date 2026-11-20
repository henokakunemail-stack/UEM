-- Device security posture (disk encryption, antivirus, firewall, local password
-- policy, screen lock) as last reported by the agent inside its inventory
-- report. An agent that predates posture sends none and has no row here: the
-- console shows that as "unknown", never as compliant.
--
-- The summary columns are what lists and reports filter on; posture_json keeps
-- the full detail. Values: on | partial | off | unknown | not_applicable for
-- the three state summaries; reported | unknown | not_applicable for the two
-- policy controls, whose thresholds are a compliance-profile decision.
CREATE TABLE IF NOT EXISTS device_posture (
    device_id        TEXT PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    posture_json     TEXT NOT NULL,
    disk_encryption  TEXT NOT NULL,
    antivirus        TEXT NOT NULL,
    firewall         TEXT NOT NULL,
    password_policy  TEXT NOT NULL,
    screen_lock      TEXT NOT NULL,
    collected_at     DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_device_posture_disk ON device_posture(disk_encryption);
CREATE INDEX IF NOT EXISTS idx_device_posture_av   ON device_posture(antivirus);
CREATE INDEX IF NOT EXISTS idx_device_posture_fw   ON device_posture(firewall);

-- One row each time a device's posture summary changes, so a report can say
-- since when a device has been out of line. Unchanged reports add nothing.
CREATE TABLE IF NOT EXISTS device_posture_history (
    id               TEXT PRIMARY KEY,
    device_id        TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    disk_encryption  TEXT NOT NULL,
    antivirus        TEXT NOT NULL,
    firewall         TEXT NOT NULL,
    password_policy  TEXT NOT NULL,
    screen_lock      TEXT NOT NULL,
    changed_at       DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_device_posture_history_device ON device_posture_history(device_id, changed_at);

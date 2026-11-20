-- Device compliance profiles and the archive of generated compliance reports.
--
-- A profile is a baseline: which controls are checked and their thresholds
-- (rules_json), and which devices it covers (scope_site / scope_group_id; both
-- empty means every active device). One profile is the default.
CREATE TABLE IF NOT EXISTS compliance_profiles (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,
    description      TEXT NOT NULL DEFAULT '',
    rules_json       TEXT NOT NULL,
    scope_site       TEXT NOT NULL DEFAULT '',
    scope_group_id   TEXT NOT NULL DEFAULT '',
    is_default       INTEGER NOT NULL DEFAULT 0,
    monthly_report   INTEGER NOT NULL DEFAULT 0,
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL
);

-- One row per generated PDF. The file itself lives under
-- DATA_DIR/compliance-reports/<id>.pdf. file_sha256 is the hash of those exact
-- bytes and is also written to the tamper-evident audit log, so a copy handed
-- to an auditor can be checked against both. content_sha256 is the hash of the
-- evaluated data the PDF was rendered from; it is the one printed inside the
-- PDF, because a file cannot carry its own hash. summary_json is kept so the
-- next report can show the trend without re-reading an old PDF.
CREATE TABLE IF NOT EXISTS compliance_reports (
    id               TEXT PRIMARY KEY,
    profile_id       TEXT NOT NULL,
    profile_name     TEXT NOT NULL,
    period           TEXT NOT NULL,
    kind             TEXT NOT NULL,              -- manual | scheduled
    generated_by     TEXT NOT NULL,              -- username, or "system"
    generated_at     DATETIME NOT NULL,
    file_sha256      TEXT NOT NULL,
    content_sha256   TEXT NOT NULL,
    size_bytes       INTEGER NOT NULL,
    summary_json     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_compliance_reports_profile ON compliance_reports(profile_id, generated_at);
CREATE INDEX IF NOT EXISTS idx_compliance_reports_file_hash ON compliance_reports(file_sha256);

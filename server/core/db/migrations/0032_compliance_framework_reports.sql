-- Regulatory framework reports (ISO 27001, UU PDP, POJK) share the compliance
-- report archive. framework_id is empty for a device compliance report; for a
-- framework report it is the template id, and summary_json then holds the
-- per-status control counts instead of the device summary.
ALTER TABLE compliance_reports ADD COLUMN framework_id   TEXT NOT NULL DEFAULT '';
ALTER TABLE compliance_reports ADD COLUMN framework_name TEXT NOT NULL DEFAULT '';

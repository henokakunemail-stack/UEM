-- Phase 22: database performance hardening & hot query indexes.

-- Hot WebSocket credential check (FindBySecretHash called on every inbound agent WebSocket frame)
CREATE INDEX IF NOT EXISTS idx_devices_secret_hash ON devices(device_secret_hash);

-- Enrollment token lookup (ConsumeEnrollmentToken)
CREATE INDEX IF NOT EXISTS idx_devices_enrollment_token ON devices(enrollment_token_hash);

-- 15-second background offline sweeper (WHERE status = 'online' AND last_seen_at < ?)
CREATE INDEX IF NOT EXISTS idx_devices_status_last_seen ON devices(status, last_seen_at);

-- 15-second background deployment task sweeper (WHERE status IN (...) AND updated_at < ?)
CREATE INDEX IF NOT EXISTS idx_deploy_tasks_status_updated ON deployment_tasks(status, updated_at);

-- Audit log tail lookup (ORDER BY created_at DESC)
CREATE INDEX IF NOT EXISTS idx_audit_created_tail ON audit_logs(created_at DESC);

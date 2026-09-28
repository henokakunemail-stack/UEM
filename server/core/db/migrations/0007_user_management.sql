-- 0007_user_management.sql
-- Phase 7: User Management (lifecycle, deactivation, display name)

ALTER TABLE users ADD COLUMN is_active INTEGER NOT NULL DEFAULT 1;
ALTER TABLE users ADD COLUMN last_login_at DATETIME;
ALTER TABLE users ADD COLUMN display_name TEXT;

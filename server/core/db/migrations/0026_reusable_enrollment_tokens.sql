-- 0026_reusable_enrollment_tokens.sql
--
-- Reusable / Multi-device enrollment tokens with configurable expiration and usage caps.
-- Allows 1 enrollment token to be used across multiple devices during mass deployment (GPO, scripts).

CREATE TABLE IF NOT EXISTS enrollment_tokens (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL DEFAULT '',
    site TEXT,
    created_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL,
    max_uses INTEGER NOT NULL DEFAULT 0, -- 0 means unlimited uses
    use_count INTEGER NOT NULL DEFAULT 0,
    created_by TEXT NOT NULL DEFAULT '',
    revoked_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_enrollment_tokens_hash ON enrollment_tokens(token_hash);
CREATE INDEX IF NOT EXISTS idx_enrollment_tokens_expires ON enrollment_tokens(expires_at);

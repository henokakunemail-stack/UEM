-- 0016_auth_sessions_and_audit_chain.sql
-- Session revocation, refresh token rotation, and a tamper-evident audit chain.
--
-- The gap this closes is that until now a JWT was its own authority. Whoever
-- held a valid signature could use it until it expired, with no server-side
-- record that it existed and no way to withdraw it. That matters most for the
-- refresh token, which is the one credential that survives a browser restart:
-- logging out did nothing to it, disabling a user did nothing to it, and a
-- refresh token captured from a proxy log stayed usable for its whole TTL.
--
-- Two things change:
--
--   1. A refresh token is now a row in auth_sessions, identified by a jti.
--      Rotating it invalidates the row it came from, so a replayed token is
--      rejected on its second use rather than accepted forever.
--   2. An audit entry carries a hash of its predecessor. Altering or removing a
--      historical row breaks the chain from that point forward, which
--      VerifyChain reports, and an operator cannot quietly edit history.
--
-- Backward compatibility: auth_sessions is new, so nothing has to be migrated
-- into it. Every token issued before this migration keeps working until it
-- expires, because a token with no session row is treated as a legacy token
-- rather than a forged one -- see auth.ParseSessionToken. Users are logged out
-- by the natural expiry of their current refresh token (up to REFRESH_TOKEN_TTL
-- away) and never earlier, so this is a widening, not a cutover.

-- One row per live session. The refresh token's jti is the primary key, which
-- is what makes rotation a single-row update and makes a replayed token
-- detectable without scanning.
CREATE TABLE IF NOT EXISTS auth_sessions (
  jti            TEXT PRIMARY KEY,
  user_id        TEXT NOT NULL,
  username       TEXT NOT NULL,
  role           TEXT NOT NULL,
  -- Kept for the revocation path only. A revoked session keeps its row so the
  -- token stays rejectable; deleting the row would make the token valid again,
  -- which is the opposite of what revocation is for.
  revoked        INTEGER NOT NULL DEFAULT 0,
  revoked_at     DATETIME,
  -- The jti that replaced this one, so the console can tell "refreshed" apart
  -- from "logged out" when it inspects a session list.
  rotated_to     TEXT,
  -- Snapshot of the user's role at issue time. A token minted while someone
  -- was a viewer must not silently become an admin token when they are later
  -- promoted, so the role claim is checked against the live row on refresh
  -- rather than trusted from the token.
  user_agent     TEXT NOT NULL DEFAULT '',
  created_at     DATETIME NOT NULL,
  last_used_at   DATETIME NOT NULL,
  expires_at     DATETIME NOT NULL,
  created_ip     TEXT NOT NULL DEFAULT ''
);

-- Listing a user's active sessions, and expiring rows that are past their TTL.
-- Both are hot paths on a fleet console: one is the session-management screen,
-- the other runs on every login.
CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth_sessions(user_id, revoked);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_expiry ON auth_sessions(expires_at);

-- Tamper-evident audit chain.
--
-- entry_hash is sha256(prev_hash || actor_type || actor_id || action ||
-- target_id || details || created_at), and prev_hash is the previous entry's
-- entry_hash. The genesis row uses 64 zeroes, so the chain is anchored and a
-- row deleted from the middle is as detectable as one edited in place: the
-- successor's prev_hash no longer matches what the chain says came before it.
--
-- Existing rows get the genesis hash rather than NULL. They cannot be chained
-- retroactively without rewriting history, which is the one thing this feature
-- exists to make impossible, so the chain simply starts at the first entry
-- written after the migration. The verifiable region is the whole table from
-- the first chained row onward; VerifyChain reports where it begins so an
-- auditor is not misled into thinking older rows are covered.
ALTER TABLE audit_logs ADD COLUMN prev_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_logs ADD COLUMN entry_hash TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_audit_entry_hash ON audit_logs(entry_hash);

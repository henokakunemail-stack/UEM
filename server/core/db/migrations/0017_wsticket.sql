-- 0017_wsticket.sql
-- One-time tickets for WebSocket handshakes.
--
-- The credential these replace is passed as ?token=<jwt> on the WebSocket URL,
-- because a browser cannot set headers on a WebSocket handshake. That is the
-- one place a JWT ends up somewhere it cannot be withdrawn from: query strings
-- are written to access logs, kept in browser history, forwarded in Referer,
-- and copied into proxy logs. A JWT recovered from any of those stays valid
-- until it expires, and unlike a session there is no row to revoke.
--
-- A ticket is the opposite: 32 random bytes, one row, 60 seconds, and it is
-- destroyed by the first successful handshake. The JWT it stands in for never
-- travels in a URL, so nothing that captures a URL captures a credential that
-- outlives the log line it was written to.
--
-- ticket_hash is the SHA-256 of the ticket, not the ticket. The table holds
-- credentials that are live right now, and a database backup, a replica, or a
-- support bundle is copied around far more freely than any of the log files
-- this change is about. Storing the digest means a copy of the table is a list
-- of already-spendable values: an attacker who has the dump has to guess a
-- 256-bit token, not read one off a disk.
--
-- There is nothing to migrate. The table is new, and existing JWT handshakes
-- keep working until the front end switches over.

CREATE TABLE IF NOT EXISTS ws_tickets (
  -- The SHA-256 hex digest of the ticket. Primary key, so redeeming a ticket is
  -- a single indexed lookup and a duplicate ticket can never be inserted.
  ticket_hash TEXT PRIMARY KEY,
  -- Who the ticket was minted for, and what it may be redeemed for. Both are
  -- opaque strings: the store does not know what a remote-exec ticket is, only
  -- that a ticket minted for one purpose must not open a socket for another.
  subject     TEXT NOT NULL,
  purpose     TEXT NOT NULL,
  created_at  DATETIME NOT NULL,
  -- Written at issue time and read in the redeem predicate, so an expired
  -- ticket is refused by the same statement that would otherwise spend it
  -- rather than by a check a concurrent caller could slip between.
  expires_at  DATETIME NOT NULL
);

-- PurgeExpired and the capacity check in Issue both scan by expiry, and both
-- run on the issue path, which is every console page load that opens a socket.
CREATE INDEX IF NOT EXISTS idx_ws_tickets_expiry ON ws_tickets(expires_at);

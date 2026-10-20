-- 0027: what a second server node needs to be safe.
--
-- Everything here is additive. On SQLite, which only ever has one node, the
-- tables stay empty apart from this node's own row and the columns are written
-- but never contended; the point is that the same schema serves both.

-- One row per running server process. A node heartbeats its row; a row that
-- has not been seen for a while belongs to a node that is gone, and whatever it
-- claimed (agent sockets, remote-control sessions) can be treated as orphaned.
CREATE TABLE IF NOT EXISTS server_nodes (
    id TEXT PRIMARY KEY,
    hostname TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL,
    last_seen_at DATETIME NOT NULL
);

-- Which node holds a device's agent socket. A request served by another node
-- uses it to route a command to the node that can actually deliver it, and a
-- node only marks a device offline when it still owns the socket -- otherwise
-- an agent that reconnected to node B is marked offline by node A's late
-- disconnect cleanup.
ALTER TABLE devices ADD COLUMN connected_node TEXT;
ALTER TABLE devices ADD COLUMN connected_at DATETIME;

-- Commands for an agent whose socket is on another node. Only the row id
-- travels in the NOTIFY (PostgreSQL caps a payload at 8000 bytes, and a script
-- deployment is larger than that); the owning node reads the row and deletes it.
CREATE TABLE IF NOT EXISTS node_outbox (
    id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL,
    device_id TEXT NOT NULL,
    payload TEXT NOT NULL,
    created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_node_outbox_node ON node_outbox(node_id, created_at);

-- Failed sign-ins per account. The per-IP limiter lives in each node's memory,
-- so with two nodes an attacker gets twice the guesses and a lockout on one node
-- is unknown to the other. This one is shared.
CREATE TABLE IF NOT EXISTS login_failures (
    username TEXT PRIMARY KEY,
    failures INTEGER NOT NULL DEFAULT 0,
    window_started_at DATETIME NOT NULL,
    locked_until DATETIME
);

-- An insertion order for audit_logs that both databases have.
--
-- The hash chain is walked and appended in (created_at, rowid) order, and rowid
-- exists only in SQLite: on PostgreSQL every audit write failed with "column
-- rowid does not exist", so the audit log never recorded anything there. seq is
-- the portable replacement, assigned under the chain lock as the previous
-- entry's seq plus one.
--
-- On SQLite the existing rows take their rowid, which is the order they were
-- chained in, so a log that verified before this migration still verifies.
ALTER TABLE audit_logs ADD COLUMN seq INTEGER;
-- @sqlite-only
UPDATE audit_logs SET seq = rowid WHERE seq IS NULL;
-- @end
-- @postgres-only
UPDATE audit_logs SET seq = numbered.n
  FROM (SELECT id, ROW_NUMBER() OVER (ORDER BY created_at, id) AS n FROM audit_logs) AS numbered
 WHERE audit_logs.id = numbered.id AND audit_logs.seq IS NULL;
-- @end
CREATE INDEX IF NOT EXISTS idx_audit_created_seq ON audit_logs(created_at, seq);

-- The exact timestamp text an entry was hashed with. SQLite keeps a timestamp
-- at whatever precision it was given (nanoseconds); PostgreSQL keeps
-- microseconds. Moving an existing log to PostgreSQL would otherwise make every
-- entry written before the move fail to recompute -- a clean log reported as
-- tampered. The migration tool fills this from the full-precision SQLite value;
-- new entries record it as they are written.
ALTER TABLE audit_logs ADD COLUMN hash_time TEXT;

-- Alert incidents need nothing here: idx_alert_dedup (0008) already allows at
-- most one active incident per rule and device, on every node.

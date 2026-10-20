package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"
)

// NodeAliveWithin is how recently a node must have heartbeated to count as
// running. Three missed heartbeats: long enough that a slow write is not a
// death, short enough that a crashed node's agents are routed elsewhere within
// the time an agent takes to reconnect.
const NodeAliveWithin = 30 * time.Second

const nodeHeartbeatEvery = 10 * time.Second

// Node is this server process's identity among the nodes sharing a database.
type Node struct {
	ID       string
	Hostname string
	Version  string
	db       *sqlx.DB
	started  time.Time
}

// NewNode creates a fresh identity. It is random per process start rather than
// derived from the hostname: two containers can share a hostname, and a node
// that restarts must not inherit claims its previous life made.
func NewNode(d *sqlx.DB, version string) *Node {
	var b [6]byte
	_, _ = rand.Read(b[:])
	host, _ := os.Hostname()
	return &Node{ID: hex.EncodeToString(b[:]), Hostname: host, Version: version, db: d, started: time.Now().UTC()}
}

// Register writes this node's row. Called before the server accepts agents, so
// the first device that claims this node can be resolved by others.
func (n *Node) Register(ctx context.Context) error {
	now := time.Now().UTC()
	_, err := n.db.ExecContext(ctx, n.db.Rebind(`
		INSERT INTO server_nodes (id, hostname, version, started_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?)`), n.ID, n.Hostname, n.Version, n.started, now)
	return err
}

// Heartbeat keeps this node's row fresh until ctx ends, and prunes rows of nodes
// that have been gone for a day, so the table stays the size of the cluster.
func (n *Node) Heartbeat(ctx context.Context) {
	tick := time.NewTicker(nodeHeartbeatEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			now := time.Now().UTC()
			res, err := n.db.ExecContext(ctx, n.db.Rebind(`UPDATE server_nodes SET last_seen_at = ? WHERE id = ?`), now, n.ID)
			if err != nil {
				log.Warn().Err(err).Msg("node heartbeat")
				continue
			}
			if rows, _ := res.RowsAffected(); rows == 0 {
				// Pruned by someone while this node was stalled; put it back.
				_ = n.Register(ctx)
			}
			_, _ = n.db.ExecContext(ctx, n.db.Rebind(`DELETE FROM server_nodes WHERE last_seen_at < ?`), now.Add(-24*time.Hour))
		}
	}
}

// Leave removes this node on a clean shutdown and releases the devices it held,
// so other nodes stop routing to it at once instead of after NodeAliveWithin.
// The devices are not marked offline: their agents reconnect to another node
// within seconds, and the offline sweep catches any that do not.
func (n *Node) Leave(ctx context.Context) {
	_, _ = n.db.ExecContext(ctx, n.db.Rebind(`UPDATE devices SET connected_node = NULL WHERE connected_node = ?`), n.ID)
	_, _ = n.db.ExecContext(ctx, n.db.Rebind(`DELETE FROM server_nodes WHERE id = ?`), n.ID)
}

// Uptime is how long this node has been running.
func (n *Node) Uptime() time.Duration { return time.Since(n.started) }

package cluster

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/rs/zerolog/log"
)

// LocalSender is the part of the agent hub the bus delivers through: a socket
// on this node, never forwarded again.
type LocalSender interface {
	SendLocal(deviceID string, msg []byte) bool
}

// SessionCache is the part of the session store the bus invalidates when
// another node revokes a session.
type SessionCache interface {
	DropCached(jti string)
}

const (
	sessionsChannel = "em_sessions"
	// outboxTTL bounds how long a forwarded command waits. A command older than
	// this was for a node that died; the agent has since reconnected elsewhere
	// and its queued work is re-sent on reconnect by the modules that queue it.
	outboxTTL = 2 * time.Minute
	// ownerCacheTTL keeps Online() from querying the database on every call.
	// It is short: a stale answer means one command waits in the outbox for a
	// node that no longer holds the device, and expires there.
	ownerCacheTTL = 2 * time.Second
	drainEvery    = 5 * time.Second
)

// Bus carries commands to agents whose socket is on another node, and session
// revocations to every node. PostgreSQL only: it is built on LISTEN/NOTIFY, and
// a SQLite deployment has a single node with nothing to route to.
//
// A forwarded command is a row in node_outbox plus a NOTIFY carrying only its
// id -- PostgreSQL caps a NOTIFY payload at 8000 bytes and a script deployment
// is larger. The owning node reads and deletes the row and sends it on its local
// socket. A periodic drain picks up anything whose NOTIFY was missed while that
// node's listener was reconnecting.
type Bus struct {
	db       *sqlx.DB
	dsn      string
	node     *Node
	local    LocalSender
	sessions SessionCache

	owners sync.Map // deviceID -> ownerEntry

	Forwarded atomic.Int64
	Delivered atomic.Int64
	connected atomic.Bool
}

type ownerEntry struct {
	node string
	at   time.Time
}

func NewBus(d *sqlx.DB, dsn string, node *Node, local LocalSender, sessions SessionCache) *Bus {
	return &Bus{db: d, dsn: dsn, node: node, local: local, sessions: sessions}
}

func (b *Bus) nodeChannel(id string) string { return "em_node_" + id }

// Connected reports whether the listener is up, for readiness.
func (b *Bus) Connected() bool { return b.connected.Load() }

// Run listens until ctx ends. It returns only on ctx cancellation or a listener
// that cannot be created at all.
func (b *Bus) Run(ctx context.Context) error {
	listener := pq.NewListener(b.dsn, time.Second, 30*time.Second, func(ev pq.ListenerEventType, err error) {
		switch ev {
		case pq.ListenerEventConnected, pq.ListenerEventReconnected:
			b.connected.Store(true)
		case pq.ListenerEventDisconnected, pq.ListenerEventConnectionAttemptFailed:
			b.connected.Store(false)
			if err != nil {
				log.Warn().Err(err).Msg("cluster bus listener disconnected")
			}
		}
	})
	defer listener.Close()
	if err := listener.Listen(b.nodeChannel(b.node.ID)); err != nil {
		return err
	}
	if err := listener.Listen(sessionsChannel); err != nil {
		return err
	}
	b.connected.Store(true)

	tick := time.NewTicker(drainEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case n := <-listener.Notify:
			if n == nil {
				// The listener reconnected; anything sent meanwhile is in
				// the outbox, and the drain collects it now.
				b.drain(ctx)
				continue
			}
			b.handle(ctx, n)
		case <-tick.C:
			b.drain(ctx)
		}
	}
}

func (b *Bus) handle(ctx context.Context, n *pq.Notification) {
	if n.Channel == sessionsChannel {
		if b.sessions != nil && n.Extra != "" {
			b.sessions.DropCached(n.Extra)
		}
		return
	}
	b.deliver(ctx, n.Extra)
}

// deliver takes one outbox row addressed to this node and sends it locally. The
// delete is the claim: only the node that removes the row delivers it, so a
// NOTIFY and a drain racing over the same row deliver it once.
func (b *Bus) deliver(ctx context.Context, id string) {
	var deviceID, payload string
	err := b.db.QueryRowxContext(ctx,
		`DELETE FROM node_outbox WHERE id = ? AND node_id = ? RETURNING device_id, payload`,
		id, b.node.ID).Scan(&deviceID, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		log.Warn().Err(err).Str("outbox", id).Msg("cluster bus: read forwarded command")
		return
	}
	if b.local.SendLocal(deviceID, []byte(payload)) {
		b.Delivered.Add(1)
		return
	}
	log.Warn().Str("device", deviceID).Msg("cluster bus: forwarded command arrived after the agent left this node")
}

func (b *Bus) drain(ctx context.Context) {
	var ids []string
	if err := b.db.SelectContext(ctx, &ids,
		`SELECT id FROM node_outbox WHERE node_id = ? ORDER BY created_at`, b.node.ID); err != nil {
		return
	}
	for _, id := range ids {
		b.deliver(ctx, id)
	}
	_, _ = b.db.ExecContext(ctx, `DELETE FROM node_outbox WHERE created_at < ?`, time.Now().UTC().Add(-outboxTTL))
}

// owner returns the live node, other than this one, holding the device's agent
// socket, or "" when there is none.
func (b *Bus) owner(deviceID string) string {
	if v, ok := b.owners.Load(deviceID); ok {
		e := v.(ownerEntry)
		if time.Since(e.at) < ownerCacheTTL {
			return e.node
		}
	}
	var node sql.NullString
	err := b.db.QueryRowx(`
		SELECT d.connected_node FROM devices d
		  JOIN server_nodes n ON n.id = d.connected_node
		 WHERE d.id = ? AND n.last_seen_at > ?`,
		deviceID, time.Now().UTC().Add(-NodeAliveWithin)).Scan(&node)
	owner := ""
	if err == nil && node.Valid && node.String != b.node.ID {
		owner = node.String
	}
	b.owners.Store(deviceID, ownerEntry{node: owner, at: time.Now()})
	return owner
}

// RemoteOnline implements transport.Router.
func (b *Bus) RemoteOnline(deviceID string) bool { return b.owner(deviceID) != "" }

// Forward implements transport.Router: it queues msg for the node holding the
// device and wakes that node. It reports false when no live node holds it, the
// same answer SendTo gives for an offline agent.
func (b *Bus) Forward(deviceID string, msg []byte) bool {
	owner := b.owner(deviceID)
	if owner == "" {
		return false
	}
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	id := hex.EncodeToString(raw[:])
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.db.ExecContext(ctx,
		`INSERT INTO node_outbox (id, node_id, device_id, payload, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, owner, deviceID, string(msg), time.Now().UTC()); err != nil {
		log.Warn().Err(err).Str("device", deviceID).Msg("cluster bus: queue forwarded command")
		return false
	}
	// A failed NOTIFY is not a lost command: the owner's drain finds the row.
	_, _ = b.db.ExecContext(ctx, `SELECT pg_notify(?, ?)`, b.nodeChannel(owner), id)
	b.Forwarded.Add(1)
	return true
}

// PublishSessionRevoked tells every node to drop its cached answer for jti
// ("*" for all). It is the session store's revoke notifier.
func (b *Bus) PublishSessionRevoked(jti string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.db.ExecContext(ctx, `SELECT pg_notify(?, ?)`, sessionsChannel, jti); err != nil {
		log.Warn().Err(err).Msg("cluster bus: broadcast session revocation")
	}
}

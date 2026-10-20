package cluster

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

type recordingSender struct {
	mu   sync.Mutex
	got  map[string][]string
	have map[string]bool
}

func (r *recordingSender) SendLocal(deviceID string, msg []byte) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.have[deviceID] {
		return false
	}
	r.got[deviceID] = append(r.got[deviceID], string(msg))
	return true
}

func (r *recordingSender) messages(deviceID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got[deviceID]...)
}

type recordingCache struct {
	mu      sync.Mutex
	dropped []string
}

func (c *recordingCache) DropCached(jti string) {
	c.mu.Lock()
	c.dropped = append(c.dropped, jti)
	c.mu.Unlock()
}

func (c *recordingCache) has(jti string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, j := range c.dropped {
		if j == jti {
			return true
		}
	}
	return false
}

func addDevice(t *testing.T, d *sqlx.DB, id, node string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := d.Exec(`INSERT INTO devices (id, hostname, os_name, status, enrolled_at, device_secret_hash, created_at, updated_at, connected_node, connected_at)
		VALUES (?, 'h', 'windows', 'online', ?, 'x', ?, ?, ?, ?)`, id, now, now, now, node, now); err != nil {
		t.Fatal(err)
	}
}

// A command for an agent whose socket is on node B, sent from node A, reaches
// the agent through B. This is what lets an operator's request land on any node.
func TestPostgres_BusDeliversACommandToTheNodeHoldingTheAgent(t *testing.T) {
	dsn := scratchPostgres(t)
	dbA, dbB := openNode(t, dsn), openNode(t, dsn)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeA, nodeB := NewNode(dbA, "test"), NewNode(dbB, "test")
	for _, n := range []*Node{nodeA, nodeB} {
		if err := n.Register(ctx); err != nil {
			t.Fatal(err)
		}
	}
	addDevice(t, dbA, "dev-on-b", nodeB.ID)

	senderA := &recordingSender{got: map[string][]string{}, have: map[string]bool{}}
	senderB := &recordingSender{got: map[string][]string{}, have: map[string]bool{"dev-on-b": true}}
	cacheB := &recordingCache{}
	busA := NewBus(dbA, dsn, nodeA, senderA, &recordingCache{})
	busB := NewBus(dbB, dsn, nodeB, senderB, cacheB)
	go busA.Run(ctx)
	go busB.Run(ctx)
	waitFor(t, "both listeners", func() bool { return busA.Connected() && busB.Connected() })
	time.Sleep(200 * time.Millisecond) // LISTEN registered before the first NOTIFY

	if !busA.RemoteOnline("dev-on-b") {
		t.Fatal("node A does not see the agent held by node B as online")
	}
	big := make([]byte, 20000) // larger than a NOTIFY payload may be
	for i := range big {
		big[i] = 'x'
	}
	for _, msg := range []string{`{"cmd":"one"}`, string(big)} {
		if !busA.Forward("dev-on-b", []byte(msg)) {
			t.Fatal("Forward refused a device held by a live node")
		}
	}
	waitFor(t, "both commands at node B", func() bool { return len(senderB.messages("dev-on-b")) == 2 })
	if got := senderB.messages("dev-on-b"); got[0] != `{"cmd":"one"}` || len(got[1]) != len(big) {
		t.Fatalf("node B received %d messages, first %q", len(got), got[0])
	}
	var left int
	if err := dbA.Get(&left, `SELECT COUNT(*) FROM node_outbox`); err != nil || left != 0 {
		t.Fatalf("outbox not emptied: %d rows (err=%v)", left, err)
	}

	// A revocation on A drops B's cached answer.
	busA.PublishSessionRevoked("jti-123")
	waitFor(t, "the revocation at node B", func() bool { return cacheB.has("jti-123") })
}

// A device held by a node that stopped heartbeating is not online, and nothing
// is queued for the dead node.
func TestPostgres_ADeadNodeOwnsNothing(t *testing.T) {
	dsn := scratchPostgres(t)
	d := openNode(t, dsn)
	ctx := context.Background()

	me, dead := NewNode(d, "test"), NewNode(d, "test")
	if err := me.Register(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dead.Register(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE server_nodes SET last_seen_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-2*NodeAliveWithin), dead.ID); err != nil {
		t.Fatal(err)
	}
	addDevice(t, d, "dev-orphan", dead.ID)

	bus := NewBus(d, dsn, me, &recordingSender{got: map[string][]string{}, have: map[string]bool{}}, &recordingCache{})
	if bus.RemoteOnline("dev-orphan") {
		t.Fatal("a device held by a dead node reads as online")
	}
	if bus.Forward("dev-orphan", []byte("x")) {
		t.Fatal("a command was queued for a dead node")
	}
}

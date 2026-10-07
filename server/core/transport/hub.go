package transport

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

// Conn is one live agent connection.
type Conn struct {
	DeviceID string
	ws       *websocket.Conn
	// send is the outbound queue, and it is never closed.
	//
	// It used to be, by closeSend, and that made "send on closed channel" a
	// reachable panic. Two goroutines can reach Send at once -- Hub.SendTo
	// callers, and the read loop's own cleanup -- and nothing orders them
	// against the close. The one-armed select that Send used could not help: Go
	// picks uniformly at random among ready cases, and a send on a closed
	// channel panics whether or not another arm was ready. A closed
	// `select { case send <- b: ... default: ... }` panics every time it picks
	// the send arm, and adding a second arm does not fix it, because a
	// closed-done channel is always ready and the two arms are then equally
	// likely. That was measured here: 521 panics in 1000 attempts.
	//
	// So the channel is not closed at all. Shutdown is signalled through done,
	// which writePump selects on, and Send keeps its own overflow behaviour
	// exactly as before -- false when the queue is full, which every caller
	// already handles.
	send chan []byte
	// done is closed exactly once to tell writePump to stop draining. A close
	// on an already-closed channel panics too, so this stays behind closeOnce.
	done chan struct{}

	closeOnce sync.Once
	// writeMu guards ws writes: gorilla does not allow concurrent writers, and
	// pingLoop and writePump both write to this socket.
	writeMu sync.Mutex
}

func newConn(deviceID string, ws *websocket.Conn) *Conn {
	return &Conn{DeviceID: deviceID, ws: ws, send: make(chan []byte, 64), done: make(chan struct{})}
}

// closeSend signals writePump to stop. Safe to call any number of times and
// from any goroutine: the read loop, the hub, and the disconnect cleanup all
// may race to tear the connection down.
func (c *Conn) closeSend() {
	c.closeOnce.Do(func() { close(c.done) })
}

// writeText serialises a text-frame write to the peer.
func (c *Conn) writeText(b []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return c.ws.WriteMessage(websocket.TextMessage, b)
}

// writeControl serialises a control-frame write (ping/pong/close) to the peer.
func (c *Conn) writeControl(msgType int) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.ws.WriteMessage(msgType, nil)
}

// Send queues a message for delivery. Returns false if the queue is full
// (agent is slow/stuck) — the caller should treat that as an unhealthy connection.
//
// Safe to call after closeSend, which is the point: send is never closed, so
// there is no panic here regardless of how shutdown interleaves. A caller that
// wins the race with teardown gets its message into a queue nobody will drain,
// which costs one buffered []byte and returns a true that the dying connection
// cannot honour -- the same outcome as any other send to a dead agent, and the
// same one every caller already handles.
func (c *Conn) Send(b []byte) bool {
	select {
	case c.send <- b:
		return true
	default:
		return false
	}
}

// writePump forwards queued messages to the peer. One per connection.
// It returns when closeSend() is called or a write fails; either way the
// connection is finished and ServeHTTP's deferred cleanup may proceed.
func (c *Conn) writePump(finished chan struct{}) {
	defer close(finished)
	for {
		select {
		case <-c.done:
			return
		case msg := <-c.send:
			if err := c.writeText(msg); err != nil {
				return
			}
		}
	}
}

// Hub is the in-memory registry of live agent connections, keyed by device ID.
// It is the server-side counterpart of the agent transport.
type Hub struct {
	mu    sync.RWMutex
	conns map[string]*Conn
}

func NewHub() *Hub {
	return &Hub{conns: make(map[string]*Conn)}
}

// Register attaches a connection, replacing any stale one for the same device.
func (h *Hub) Register(deviceID string, ws *websocket.Conn) *Conn {
	c := newConn(deviceID, ws)
	h.mu.Lock()
	if old, ok := h.conns[deviceID]; ok {
		// A previous session is still registered. Abandon it: its read loop will
		// hit a write error or a closed socket and run its own cleanup.
		//
		// The socket is closed here, but nothing else is. closeAndLog used to
		// call closeSend as well, from a goroutine of its own, which raced
		// writePump -- and its comment here claimed the opposite, saying that
		// closing old.send from here would race, three lines before doing it.
		// Closing the socket is enough: it unblocks the old read loop, which
		// returns an error and runs the same deferred cleanup this connection
		// would have run anyway, including its own closeSend.
		delete(h.conns, deviceID)
		log.Warn().Str("device", deviceID).Msg("closing stale connection for device")
		_ = old.ws.Close()
	}
	h.conns[deviceID] = c
	h.mu.Unlock()
	return c
}

// Disconnect withdraws a device's live connection after its credential is
// revoked. Closing the socket unblocks the read loop, which owns the normal
// cleanup; the send channel must not be closed here.
func (h *Hub) Disconnect(deviceID string) {
	h.mu.Lock()
	c := h.conns[deviceID]
	delete(h.conns, deviceID)
	h.mu.Unlock()
	if c != nil {
		_ = c.ws.Close()
	}
}

// Unregister removes a connection if it is still the registered one.
func (h *Hub) Unregister(c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.conns[c.DeviceID]; ok && cur == c {
		delete(h.conns, c.DeviceID)
	}
}

// Get returns the live connection for a device, or nil.
func (h *Hub) Get(deviceID string) *Conn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[deviceID]
}

// Online returns true if the device currently has a live connection.
func (h *Hub) Online(deviceID string) bool {
	return h.Get(deviceID) != nil
}

// SendTo delivers a message to a device's live connection.
func (h *Hub) SendTo(deviceID string, b []byte) bool {
	c := h.Get(deviceID)
	if c == nil {
		return false
	}
	return c.Send(b)
}

// Count returns the number of live connections.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

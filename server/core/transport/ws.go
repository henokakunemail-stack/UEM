package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// WSHandler upgrades agent connections and drives the per-connection read loop.
type WSHandler struct {
	hub          *Hub
	upgrader     websocket.Upgrader
	repo         *devicemgmt.Repository
	db           *sqlx.DB
	offlineAfter time.Duration
	flusher      *HeartbeatFlusher
	// inventory accepts agent collection reports. Optional: nil means reports are
	// logged and dropped, which keeps Phase 1 deployments working unchanged.
	inventory InventoryReceiver
	// terminal accepts interactive terminal data from agents (Phase 5).
	terminal TerminalReceiver
	// setCapabilities stores the capability list advertised in hello.
	setCapabilities func(ctx context.Context, deviceID, capabilitiesJSON string) error
	// updateQueue delivers agent update tasks that were queued while the device
	// was offline. Optional: nil means queued updates wait for the next manual
	// dispatch, which is the behaviour before this hook existed.
	updateQueue UpdateQueue
	// filterSync pushes the effective filter policy to a device that reconnected
	// without it having been applied. Optional, for the same reason as updateQueue.
	//
	// Without this, a policy aimed at a laptop that happened to be asleep was never
	// delivered: the manual sync reported "queued until it reconnects", wrote
	// status='pending', and returned. Nothing read that row, so the device stayed
	// unfiltered until an operator clicked Sync again.
	filterSync FilterSyncer
}

// FilterSyncer re-sends the compiled filter policy to a device that has just
// come back online. It must be a no-op when the device already holds the current
// version, so a reconnect storm does not re-push an identical rule set to every
// device in the fleet.
type FilterSyncer interface {
	SyncOnReconnect(ctx context.Context, deviceID string) error
}

// UpdateQueue delivers the update tasks left pending for a device. It is called
// once, right after the device is marked online, so a task an operator queued
// against a laptop that was asleep actually gets sent instead of sitting in the
// database claiming to be 'dispatched' forever.
type UpdateQueue interface {
	FlushPendingUpdates(ctx context.Context, deviceID string) error
}

// TerminalReceiver relays interactive terminal data and closure events from an agent to the server.
type TerminalReceiver interface {
	AcceptTerminalData(ctx context.Context, deviceID string, sessionID string, data string) error
	AcceptTerminalClose(ctx context.Context, deviceID string, sessionID string) error
}

// InventoryReceiver stores an inventory report. The transport passes raw JSON:
// decoding into agent types is not possible server-side because those types
// carry OS build tags, and the server must build on every platform.
type InventoryReceiver interface {
	AcceptInventory(ctx context.Context, deviceID string, raw []byte) error
}

// NewWSHandler builds a handler with no inventory receiver; use
// (WSHandler).WithInventory to enable Phase 2 collection.
func NewWSHandler(hub *Hub, repo *devicemgmt.Repository, db *sqlx.DB, offlineAfter time.Duration) *WSHandler {
	var flusher *HeartbeatFlusher
	if db != nil {
		flusher = NewHeartbeatFlusher(db, 2*time.Second)
	}
	return &WSHandler{
		hub: hub, repo: repo, db: db, offlineAfter: offlineAfter,
		flusher: flusher,
		upgrader: websocket.Upgrader{
			CheckOrigin: auth.ValidateWebSocketOrigin,
		},
	}
}

// WithInventory attaches the Phase 2 inventory receiver so agent collection
// reports are persisted instead of dropped.
func (h *WSHandler) WithInventory(r InventoryReceiver) *WSHandler {
	h.inventory = r
	if sc, ok := r.(capabilitiesSetter); ok {
		h.setCapabilities = sc.SetCapabilities
	}
	return h
}

// WithUpdateQueue attaches the handler that delivers update tasks queued while
// a device was offline. Without it, a queued update waits for the operator to
// press Dispatch again -- the row is left in its pending state and nothing ever
// re-reads it.
func (h *WSHandler) WithUpdateQueue(q UpdateQueue) *WSHandler {
	h.updateQueue = q
	return h
}

// WithFilterSync attaches the re-sync hook for the web filter policy. Without it,
// a device that was offline when the operator synced stays unfiltered until the
// next manual sync.
func (h *WSHandler) WithFilterSync(s FilterSyncer) *WSHandler {
	h.filterSync = s
	return h
}

// WithTerminal attaches the Phase 5 terminal receiver for interactive shell streaming.
func (h *WSHandler) WithTerminal(t TerminalReceiver) *WSHandler {
	h.terminal = t
	return h
}

// WithOriginChecker replaces the default CSWSH origin policy. The operator
// builds this from ALLOWED_ORIGIN_DOMAINS so a console served from a different
// hostname can still open WebSockets.
func (h *WSHandler) WithOriginChecker(check auth.OriginChecker) *WSHandler {
	if check != nil {
		h.upgrader.CheckOrigin = check
	}
	return h
}

// capabilitiesSetter is implemented by the inventory receiver when it can store
// the capability list. Discovered by assertion so the Phase 1-only wiring keeps
// working without it.
type capabilitiesSetter interface {
	SetCapabilities(ctx context.Context, deviceID, capabilitiesJSON string) error
}

// ServeHTTP handles GET /api/agent/connect.
// Headers: X-Device-Id, X-Device-Secret.
func (h *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	deviceID := r.Header.Get("X-Device-Id")
	secret := r.Header.Get("X-Device-Secret")
	if deviceID == "" || secret == "" {
		http.Error(w, "missing device credentials", http.StatusUnauthorized)
		return
	}

	secretHash := devicemgmt.HashToken(secret)
	dev, err := h.repo.FindBySecretHash(r.Context(), secretHash)
	if errors.Is(err, devicemgmt.ErrNotFound) {
		http.Error(w, "invalid device credentials", http.StatusUnauthorized)
		return
	}
	if err != nil {
		log.Error().Err(err).Msg("lookup device secret")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if dev.ID != deviceID {
		// Secret is valid but bound to a different device ID: reject.
		http.Error(w, "device id mismatch", http.StatusUnauthorized)
		return
	}

	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Str("device", deviceID).Msg("ws upgrade")
		return
	}

	c := h.hub.Register(deviceID, ws)
	// Retire or rotation may have committed between the pre-upgrade lookup
	// and Register; never leave that already-revoked socket in the hub.
	if !h.credentialIsLive(r.Context(), deviceID, secretHash) {
		h.hub.Unregister(c)
		_ = ws.Close()
		return
	}

	// done is closed by writePump when it stops. The cleanup below waits for
	// that before closing the socket, so it must exist before the defer runs.
	done := make(chan struct{})

	defer func() {
		h.hub.Unregister(c)
		// Stop the write pump first; once it has exited it is safe to close the
		// socket. Order matters: closing the socket while writePump may still be
		// writing would race gorilla's single-writer rule.
		c.closeSend()
		<-done
		_ = ws.Close()
		if h.flusher != nil {
			h.flusher.Remove(deviceID)
		}
		// On disconnect the device is offline; last_seen keeps the timestamp.
		// r.Context() is still live here: the goroutine it guards — this very
		// handler — has not returned yet, so the DB write is not orphaned.
		_ = h.repo.UpdateStatus(r.Context(), deviceID, devicemgmt.StatusOffline, time.Now().UTC())
		_ = audit.Log(r.Context(), h.db, "agent", deviceID, "agent.disconnect", deviceID, nil)
		log.Info().Str("device", deviceID).Msg("agent disconnected")
	}()

	// Dead-line + ping/pong keep the disconnect handler responsive. Without it,
	// a half-open TCP connection (e.g. agent killed mid-session, or a NAT that
	// silently dropped the flow) would leave the read loop blocked and the
	// device wrongly "online" until the OS notices — minutes or longer.
	ws.SetReadLimit(1 << 20) // 1 MiB; larger frames are protocol errors
	_ = ws.SetReadDeadline(time.Now().Add(h.readDeadline()))
	go h.pingLoop(c)
	go c.writePump(done)

	_ = audit.Log(r.Context(), h.db, "agent", deviceID, "agent.connect", deviceID, nil)

	// Mark online only while the device is active. A concurrent retirement may
	// have won after the credential recheck; do not dispatch queued work then.
	if err := h.repo.UpdateStatus(r.Context(), deviceID, devicemgmt.StatusOnline, time.Now().UTC()); err != nil {
		log.Warn().Err(err).Str("device", deviceID).Msg("refusing agent connection without active device")
		return
	}
	if !h.credentialIsLive(r.Context(), deviceID, secretHash) {
		return
	}

	// Flush whatever was queued while this device was away. After the status
	// update, not before: the queue flush reads the release and sends on this
	// socket, and SendTask marks the row sent before the write, so a failure
	// here leaves the task pending for the next reconnect rather than losing it.
	// A flush error is logged and swallowed -- the connection is live either
	// way, and returning would tear down a healthy agent over a delivery
	// problem that the pending set already accounts for.
	if h.updateQueue != nil {
		if err := h.updateQueue.FlushPendingUpdates(r.Context(), deviceID); err != nil {
			log.Warn().Err(err).Str("device", deviceID).Msg("flush pending agent updates")
		}
	}

	// Same reasoning for the filter policy: a sync clicked while the device was
	// offline never reached it. Ordered after the status update because this
	// dispatches over this socket.
	if h.filterSync != nil {
		if err := h.filterSync.SyncOnReconnect(r.Context(), deviceID); err != nil {
			log.Warn().Err(err).Str("device", deviceID).Msg("re-sync filter policy on reconnect")
		}
	}

	// readLoop returns when the socket breaks. The deferred closure above then
	// closes the send channel, waits for writePump to notice, closes the socket
	// and marks the device offline. Waiting for done *here* as well would
	// deadlock: closeSend is called from that same closure, which cannot run
	// while this frame is still blocked.
	h.readLoop(r.Context(), c, ws, secretHash)
}

// readDeadline returns the read timeout used for the agent connection. Chosen
// relative to the agent heartbeat so a live agent always refreshes it in time.
func (h *WSHandler) readDeadline() time.Duration {
	d := h.offlineAfter
	if d < 30*time.Second {
		return 30 * time.Second
	}
	return d
}

// pingLoop sends websocket pings so a silently broken connection is detected
// within roughly one period instead of relying on TCP keepalive.
func (h *WSHandler) pingLoop(c *Conn) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := c.writeControl(websocket.PingMessage); err != nil {
			return
		}
	}
}

// credentialIsLive checks the current row, not the identity accepted when the
// WebSocket first upgraded. Retirement and rotation both invalidate that row.
func (h *WSHandler) credentialIsLive(ctx context.Context, deviceID, secretHash string) bool {
	dev, err := h.repo.FindBySecretHash(ctx, secretHash)
	if err != nil {
		if !errors.Is(err, devicemgmt.ErrNotFound) {
			log.Error().Err(err).Str("device", deviceID).Msg("check agent credential")
		}
		return false
	}
	return dev.ID == deviceID
}

// readLoop handles inbound agent messages until the connection breaks.
func (h *WSHandler) readLoop(ctx context.Context, c *Conn, ws *websocket.Conn, secretHash string) {
	// A pong from the agent refreshes the read deadline, proving the
	// connection is alive without relying on application-level heartbeats alone.
	ws.SetPongHandler(func(string) error {
		_ = ws.SetReadDeadline(time.Now().Add(h.readDeadline()))
		return nil
	})
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return // client closed or error
		}
		if !h.credentialIsLive(ctx, c.DeviceID, secretHash) {
			return // revoked after upgrade; process no more frames
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			log.Warn().Str("device", c.DeviceID).Bytes("msg", data).Msg("bad message")
			continue
		}
		switch env.Type {
		case TypeHello:
			h.handleHello(ctx, c, env)
		case TypeHeartbeat:
			h.handleHeartbeat(ctx, c)
		case TypeCommandResult:
			h.handleCommandResult(ctx, c, env)
		case TypeInventory:
			h.handleInventory(ctx, c, env)
		case TypeTermData:
			h.handleTermData(ctx, c, env)
		case TypeTermClose:
			h.handleTermClose(ctx, c, env)
		default:
			log.Warn().Str("device", c.DeviceID).Str("type", env.Type).Msg("unknown message type")
		}
	}
}

// handleTermData routes incoming terminal stdout data from the agent to the active operator session.
func (h *WSHandler) handleTermData(ctx context.Context, c *Conn, env Envelope) {
	if h.terminal == nil {
		return
	}
	var p struct {
		SessionID string `json:"session_id"`
		Data      string `json:"data"`
	}
	b, _ := json.Marshal(env.Payload)
	_ = json.Unmarshal(b, &p)
	if p.SessionID == "" {
		p.SessionID = env.ID
	}
	if p.SessionID != "" && p.Data != "" {
		_ = h.terminal.AcceptTerminalData(ctx, c.DeviceID, p.SessionID, p.Data)
	}
}

// handleTermClose handles terminal closure notification from the agent.
func (h *WSHandler) handleTermClose(ctx context.Context, c *Conn, env Envelope) {
	if h.terminal == nil {
		return
	}
	sessionID := env.ID
	if sessionID == "" {
		var p struct {
			SessionID string `json:"session_id"`
		}
		b, _ := json.Marshal(env.Payload)
		_ = json.Unmarshal(b, &p)
		sessionID = p.SessionID
	}
	if sessionID != "" {
		_ = h.terminal.AcceptTerminalClose(ctx, c.DeviceID, sessionID)
	}
}

// handleInventory stores a collection report. The handler is optional: if the
// wiring does not provide one, the report is logged and dropped rather than
// breaking the connection.
func (h *WSHandler) handleInventory(ctx context.Context, c *Conn, env Envelope) {
	if h.inventory == nil {
		log.Debug().Str("device", c.DeviceID).Msg("inventory received but no handler wired")
		return
	}
	payload, _ := json.Marshal(env.Payload)
	if err := h.inventory.AcceptInventory(ctx, c.DeviceID, payload); err != nil {
		log.Warn().Err(err).Str("device", c.DeviceID).Msg("accept inventory")
	}
}

func (h *WSHandler) handleHello(ctx context.Context, c *Conn, env Envelope) {
	// The agent sends its osinfo.Info struct flat: {name, version, hostname,
	// agent_version, capabilities}. Older payloads omit capabilities, so a
	// mixed-version fleet does not break the inventory on upgrade.
	b, _ := json.Marshal(env.Payload)
	var p struct {
		AgentVersion string   `json:"agent_version"`
		Name         string   `json:"name"`
		Version      string   `json:"version"`
		Hostname     string   `json:"hostname"`
		Capabilities []string `json:"capabilities"`
		OS           *struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"os"`
	}
	_ = json.Unmarshal(b, &p)
	name, version := p.Name, p.Version
	if p.OS != nil {
		if name == "" {
			name = p.OS.Name
		}
		if version == "" {
			version = p.OS.Version
		}
	}
	if name == "" {
		name = "unknown"
	}
	_ = h.repo.UpdateOSInfo(ctx, c.DeviceID, version, p.AgentVersion)
	_ = h.repo.UpdateStatus(ctx, c.DeviceID, devicemgmt.StatusOnline, time.Now().UTC())
	if h.setCapabilities != nil && len(p.Capabilities) > 0 {
		if capJSON, err := json.Marshal(p.Capabilities); err == nil {
			_ = h.setCapabilities(ctx, c.DeviceID, string(capJSON))
		}
	}
	log.Info().Str("device", c.DeviceID).Str("agent", p.AgentVersion).
		Str("os", name+" "+version).Str("hostname", p.Hostname).
		Int("capabilities", len(p.Capabilities)).Msg("agent hello")
}

func (h *WSHandler) handleHeartbeat(ctx context.Context, c *Conn) {
	if h.flusher != nil {
		h.flusher.Record(c.DeviceID)
	} else {
		_ = h.repo.UpdateStatus(ctx, c.DeviceID, devicemgmt.StatusOnline, time.Now().UTC())
	}
}

// FlushHeartbeats synchronously flushes all buffered heartbeats to the database.
func (h *WSHandler) FlushHeartbeats() {
	if h.flusher != nil {
		h.flusher.Flush()
	}
}

// Close gracefully flushes remaining heartbeats and terminates background workers.
func (h *WSHandler) Close() {
	if h.flusher != nil {
		h.flusher.Close()
	}
}

func (h *WSHandler) handleCommandResult(ctx context.Context, c *Conn, env Envelope) {
	// Phase 1 only records the result; full command tracking comes with the queue.
	var resultJSON string
	if env.Result != nil {
		b, _ := json.Marshal(env.Result)
		resultJSON = string(b)
	}
	status := "done"
	if env.Status == "failed" {
		status = "failed"
	}
	_, err := h.db.ExecContext(ctx, `
		UPDATE agent_commands SET status = ?, completed_at = ?, result = ? WHERE id = ?`,
		status, time.Now().UTC(), resultJSON, env.ID)
	if err != nil {
		log.Error().Err(err).Str("device", c.DeviceID).Str("cmd", env.ID).Msg("update command result")
	}
}

var _ = sql.ErrNoRows // kept for future repository error mapping

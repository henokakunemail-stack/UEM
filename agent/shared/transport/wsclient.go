// Package transport is the agent-side connection to the central server.
// The agent always initiates the connection (outbound), satisfying the
// constraint that the server never reaches into branch networks.
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
)

// Client is the persistent agent->server connection with automatic reconnect.
type Client struct {
	serverURL    string // e.g. "ws://localhost:8443"
	deviceID     string
	deviceSecret string

	helloExtra any // extra fields merged into the hello message

	mu        sync.Mutex
	ws        *websocket.Conn
	closed    bool
	onCommand func(ctx context.Context, command, id string, payload json.RawMessage) any
	onCollect func(ctx context.Context)
}

func NewClient(serverURL, deviceID, deviceSecret string) *Client {
	return &Client{
		serverURL:    wsURL(serverURL),
		deviceID:     deviceID,
		deviceSecret: deviceSecret,
	}
}

// DefaultHeartbeat is the agent heartbeat cadence when the caller does not
// choose one. It is deliberately shorter than the server's AGENT_OFFLINE_AFTER
// default (90s) so a healthy device is seen well inside the window that would
// otherwise mark it offline.
const DefaultHeartbeat = 20 * time.Second

// wsURL normalises an http(s):// server base URL to ws(s):// so callers can use
// the same "-server" value for both the REST enrollment call and the socket.
func wsURL(serverURL string) string {
	if s := strings.TrimPrefix(serverURL, "https://"); s != serverURL {
		return "wss://" + s
	}
	if s := strings.TrimPrefix(serverURL, "http://"); s != serverURL {
		return "ws://" + s
	}
	// Already ws://, wss://, or a bare host:port — use as-is.
	return strings.TrimRight(serverURL, "/")
}

// SetCommandHandler installs the callback invoked for each server command.
// The returned value is sent back as the command result.
func (c *Client) SetCommandHandler(h func(ctx context.Context, command, id string, payload json.RawMessage) any) {
	c.onCommand = h
}

// SetHelloExtra merges extra fields into the hello message sent on each connect.
// Used for the capability advertisement; it must be plain JSON-able data.
func (c *Client) SetHelloExtra(v any) { c.helloExtra = v }

// Close stops the client. Run blocks until ctx is cancelled or Close is called.
func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	if c.ws != nil {
		_ = c.ws.Close()
	}
	c.mu.Unlock()
}

// Run connects and stays connected until ctx is done. Between failed attempts it
// backs off with jitter so 500 agents never retry in lockstep after an outage.
//
// heartbeat sets the TypeHeartbeat cadence; pass DefaultHeartbeat to keep the
// standard interval. It is a parameter rather than a field because it is fixed
// for the life of the connection, and a setter would race the heartbeat loop.
func (c *Client) Run(ctx context.Context, hello any, heartbeat time.Duration) error {
	if heartbeat <= 0 {
		heartbeat = DefaultHeartbeat
	}
	backoff := time.Second
	const maxBackoff = 60 * time.Second

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		connStart := time.Now()
		err := c.connectAndServe(ctx, hello, heartbeat)
		if c.isClosed() {
			// Graceful shutdown requested via Close(): do not reconnect.
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Reset backoff if the previous connection was stable for at least 30s.
		if time.Since(connStart) > 30*time.Second {
			backoff = time.Second
		}

		// Exponential backoff with full jitter.
		jitter := time.Duration(0)
		if backoff > time.Second {
			jitter = time.Duration(time.Now().UnixNano()%int64(backoff)) - backoff/2
		}
		sleep := backoff + jitter
		log.Warn().Err(err).Dur("retry_in", sleep).Msg("disconnected, retrying")
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *Client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *Client) connectAndServe(ctx context.Context, hello any, heartbeat time.Duration) error {
	header := http.Header{}
	header.Set("X-Device-Id", c.deviceID)
	header.Set("X-Device-Secret", c.deviceSecret)

	url := c.serverURL + "/api/agent/connect"
	dialer := NewWebSocketDialer(15 * time.Second)
	ws, _, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		return fmt.Errorf("dial server: %w", err)
	}
	c.mu.Lock()
	c.ws = ws
	c.mu.Unlock()
	defer func() {
		_ = ws.Close()
		c.mu.Lock()
		c.ws = nil
		c.mu.Unlock()
	}()

	// Hello carries the OS facts plus anything SetHelloExtra added (capabilities).
	// The merge keeps the server-side hello handler unchanged: it decodes known
	// fields and ignores the rest.
	helloPayload := hello
	if c.helloExtra != nil {
		helloPayload = mergeHello(hello, c.helloExtra)
	}
	if err := c.send(Envelope{Type: TypeHello, Payload: helloPayload}); err != nil {
		return err
	}
	log.Info().Str("server", c.serverURL).Msg("connected to server")

	// Answer server pings so a half-open connection is detected on both ends.
	// Gorilla handles control frames in ReadMessage; extending the read deadline
	// on incoming ping keeps the connection alive even during idle periods.
	ws.SetPingHandler(func(appData string) error {
		_ = ws.SetReadDeadline(time.Now().Add(45 * time.Second))
		return ws.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})
	ws.SetPongHandler(func(string) error {
		_ = ws.SetReadDeadline(time.Now().Add(45 * time.Second))
		return nil
	})
	_ = ws.SetReadDeadline(time.Now().Add(45 * time.Second))

	// Heartbeat keeps the connection warm through NATs and updates last_seen.
	var heartbeatDone sync.WaitGroup
	heartbeatDone.Add(1)
	go func() {
		defer heartbeatDone.Done()
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := c.send(Envelope{Type: TypeHeartbeat}); err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	defer heartbeatDone.Wait()

	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		// Any traffic proves the connection is alive; extend the deadline.
		_ = ws.SetReadDeadline(time.Now().Add(45 * time.Second))
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			log.Warn().Bytes("msg", data).Msg("bad message from server")
			continue
		}
		if env.Type == TypeCommand && c.onCommand != nil {
			c.handleCommand(ctx, env)
		}
		if env.Type == TypeInventoryCollect && c.onCollect != nil {
			// On-demand collection: an admin opened the device detail page. Run on
			// a fresh goroutine so a slow CIM call on Windows cannot stall this
			// read loop past its read deadline.
			go c.onCollect(ctx)
		}
	}
}

// Deferred is the handler result that means "this agent will send the reply".
//
// handleCommand normally answers a command the moment its handler returns, which
// is right for everything that finishes inline. A handler that queues work and
// returns cannot use that: the reply would land first and record the command
// done while the work is still pending, and if the agent then died mid-task the
// row would say the operation succeeded when nothing had run yet. Handlers that
// own their reply return this and call Send with a TypeCommandResult envelope
// themselves, once there is a real outcome to report.
type Deferred struct{}

// handleCommand dispatches one server command and replies with its result.
func (c *Client) handleCommand(ctx context.Context, env Envelope) {
	var raw json.RawMessage
	if env.Payload != nil {
		raw, _ = json.Marshal(env.Payload)
	}
	res := c.onCommand(ctx, env.Command, env.ID, raw)
	if _, ok := res.(Deferred); ok {
		return
	}
	status := StatusDone
	if res == nil {
		status = StatusFailed
	} else if s, ok := res.(map[string]string); ok && s["status"] == "rejected" {
		// A handler that answers {"status": "rejected"} has NOT run the work --
		// the queue was full, or the payload was unusable. Reporting that as done
		// writes status='done' to the command row for an operation that never
		// started, and the row is the only record the console has. The word to
		// match on is the same one these handlers already send.
		status = StatusFailed
	}
	_ = c.send(Envelope{Type: TypeCommandResult, ID: env.ID, Status: status, Result: res})
}

// SetCollectHandler installs the callback for the inventory.collect request.
// The callback performs a collection and reports the result over the socket.
func (c *Client) SetCollectHandler(h func(ctx context.Context)) {
	c.onCollect = h
}

// ReportInventory sends a collected inventory report to the server. The typed
// signature keeps the client honest about what it ships; the envelope payload is
// marshalled generically like any other message.
func (c *Client) ReportInventory(ctx context.Context, rep inventory.Report) error {
	return c.send(Envelope{Type: TypeInventory, Payload: rep})
}

// mergeHello combines the OS facts with extra hello fields, extra winning on a
// key collision (the caller is the agent's own configuration, not the OS).
func mergeHello(base, extra any) map[string]any {
	out := map[string]any{}
	if b, err := json.Marshal(base); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	if e, err := json.Marshal(extra); err == nil {
		var m map[string]any
		if err := json.Unmarshal(e, &m); err == nil {
			for k, v := range m {
				out[k] = v
			}
		}
	}
	return out
}

// Send transmits an envelope to the server.
func (c *Client) Send(e Envelope) error { return c.send(e) }

func (c *Client) send(e Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ws == nil {
		return errors.New("not connected")
	}
	_ = c.ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return c.ws.WriteMessage(websocket.TextMessage, b)
}

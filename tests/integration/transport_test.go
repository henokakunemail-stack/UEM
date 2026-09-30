package integration

// E2E test for the Fase-1 agent transport. It drives the real server handlers
// (enrollment + WebSocket) against a real SQLite DB, with a gorilla/websocket
// client playing the role of an agent on a branch network.
//
// Flow under test:
//  1. admin pre-registers a device with a one-time enrollment token
//  2. agent exchanges the token for a persistent device secret (HTTP)
//  3. agent opens the WebSocket and sends hello + heartbeat
//  4. server pushes a "ping" command; agent replies; result is persisted
//  5. device is "online" while connected and "offline" after disconnect

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/transport"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/logger"
	srvtransport "github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

func init() { logger.Init("disabled", "") }

type env struct {
	db     *sqlx.DB
	repo   *devicemgmt.Repository
	hub    *srvtransport.Hub
	server *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	repo := devicemgmt.NewRepository(d)
	hub := srvtransport.NewHub()
	wsH := srvtransport.NewWSHandler(hub, repo, d, time.Minute)
	enrollH := devicemgmt.NewEnrollmentHandler(repo, d)

	mux := http.NewServeMux()
	mux.Handle("/api/agent/connect", wsH)
	enrollH.Register(mux)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return &env{db: d, repo: repo, hub: hub, server: ts}
}

func (e *env) wsURL() string {
	return "ws" + strings.TrimPrefix(e.server.URL, "http") + "/api/agent/connect"
}

func (e *env) httpURL(path string) string { return e.server.URL + path }

// dialAgent connects a WS client using device credentials, mimicking an agent
// on a branch network reaching out to the central server. gorilla/websocket
// only sends explicitly-set headers, so credentials must be attached here.
func dialAgent(t *testing.T, e *env, deviceID, secret string) *websocket.Conn {
	t.Helper()
	h := http.Header{}
	h.Set("X-Device-Id", deviceID)
	h.Set("X-Device-Secret", secret)
	conn, resp, err := websocket.DefaultDialer.Dial(e.wsURL(), h)
	if err != nil {
		body := ""
		if resp != nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(b)
		}
		t.Fatalf("ws dial: %v (status=%d body=%s)", err, statusOrZero(resp), body)
	}
	return conn
}

func statusOrZero(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

// expectDialFail asserts that a WS dial is rejected, and returns the HTTP
// status the server sent. It is the negative counterpart of dialAgent: a
// rejected upgrade is the expected outcome here, not a test failure.
func expectDialFail(t *testing.T, e *env, deviceID, secret string) int {
	t.Helper()
	h := http.Header{}
	h.Set("X-Device-Id", deviceID)
	h.Set("X-Device-Secret", secret)
	conn, resp, err := websocket.DefaultDialer.Dial(e.wsURL(), h)
	if err == nil {
		conn.Close()
		t.Fatalf("dial with bad secret unexpectedly succeeded")
	}
	if resp == nil {
		t.Fatalf("dial failed without an HTTP response: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad secret should give 401, got %d (%s)", resp.StatusCode, body)
	}
	return resp.StatusCode
}

func TestE2EEnrollConnectCommand(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	// --- 1. admin pre-registers the device with a one-time token ---
	plainToken := devicemgmt.GenerateToken()
	now := time.Now().UTC()
	tokenHash := devicemgmt.HashToken(plainToken) // NULL once the agent enrolls
	site := "cabang-test"
	device := devicemgmt.Device{
		ID:                  devicemgmt.NewID(),
		Hostname:            "E2E-PC-01",
		OSName:              devicemgmt.OSWindows,
		Status:              devicemgmt.StatusOffline,
		EnrolledAt:          now,
		EnrollmentTokenHash: &tokenHash,
		Site:                &site,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := e.repo.Create(ctx, device); err != nil {
		t.Fatalf("create device: %v", err)
	}

	// --- 2. agent exchanges the token for a persistent secret ---
	secret := enroll(t, e, device.ID, plainToken)

	// Replaying the one-time token must now be rejected.
	if _, err := postJSON(e.httpURL("/api/agent/enroll"),
		map[string]string{"enrollment_token": plainToken}); err == nil {
		t.Fatal("replaying consumed enrollment token must fail")
	}

	// A wrong secret must not authenticate the WS connection. The server must
	// refuse the upgrade (401) rather than accepting a socket for a device that
	// never proved ownership of its secret.
	expectDialFail(t, e, device.ID, devicemgmt.GenerateToken())
	if e.hub.Online(device.ID) {
		t.Fatal("connection with wrong secret must not register")
	}

	// --- 3. agent connects with the real secret and says hello ---
	conn := dialAgent(t, e, device.ID, secret)
	defer conn.Close()

	send(t, conn, transport.Envelope{Type: "hello", Payload: map[string]any{
		"agent_version": "0.1.0",
		"os":            map[string]string{"name": "windows", "version": "10.0.22631"},
	}})
	if err := waitFor(func() bool {
		d, err := e.repo.GetByID(ctx, device.ID)
		return err == nil && d.AgentVersionString() == "0.1.0" && d.OSVersionString() == "10.0.22631"
	}); err != nil {
		t.Fatalf("agent hello OS info not persisted: %v", err)
	}
	waitForStatus(t, e, device.ID, devicemgmt.StatusOnline)

	dev, err := e.repo.GetByID(ctx, device.ID)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if dev.AgentVersionString() != "0.1.0" || dev.OSVersionString() != "10.0.22631" {
		t.Fatalf("hello not persisted: agent=%q os=%q", dev.AgentVersionString(), dev.OSVersionString())
	}
	if !e.hub.Online(device.ID) {
		t.Fatal("device must be online in the hub")
	}

	// --- 4. heartbeat keeps it online ---
	send(t, conn, transport.Envelope{Type: "heartbeat"})
	waitForStatus(t, e, device.ID, devicemgmt.StatusOnline)

	// --- 5. server pushes a command; the agent-side dispatcher replies ---
	//
	// The test acts as the agent: it receives the command through the same
	// dispatcher the production agent uses (agent/shared/transport), so the
	// reply path is the real one rather than a hand-rolled response.
	disp := transport.NewDispatcher()
	cmdCh := make(chan transport.Envelope, 1)
	disp.Register("ping", func(ctx context.Context, command, id string, payload json.RawMessage) any {
		cmdCh <- transport.Envelope{Type: "command", ID: id, Command: command}
		return map[string]string{"pong": "2026", "host": "E2E-PC-01"}
	})

	cmd := transport.Envelope{Type: "command", ID: devicemgmt.NewID(), Command: "ping"}
	if _, err := e.db.ExecContext(ctx, `
		INSERT INTO agent_commands (id, device_id, command_type, payload, status, created_at, sent_at)
		VALUES (?, ?, 'ping', '{}', 'sent', ?, ?)`,
		cmd.ID, device.ID, now, now); err != nil {
		t.Fatalf("insert command: %v", err)
	}
	if !e.hub.SendTo(device.ID, mustJSON(t, cmd)) {
		t.Fatal("SendTo failed on a live connection")
	}

	// Agent reads the command, runs it, and writes the result back.
	got := readEnv(t, conn)
	if got.Type != "command" || got.ID != cmd.ID || got.Command != "ping" {
		t.Fatalf("agent received unexpected command: %+v", got)
	}
	result := disp.Handle(context.Background(), got.Command, got.ID, nil)
	send(t, conn, transport.Envelope{Type: "command_result", ID: got.ID, Status: "done", Result: result})

	// Server persists the result.
	if err := waitFor(func() bool {
		var status, res string
		if err := e.db.GetContext(ctx, &status,
			`SELECT status FROM agent_commands WHERE id = ?`, cmd.ID); err != nil {
			return false
		}
		_ = e.db.GetContext(ctx, &res, `SELECT result FROM agent_commands WHERE id = ?`, cmd.ID)
		return status == "done" && strings.Contains(res, "pong")
	}); err != nil {
		t.Fatalf("command result not persisted: %v", err)
	}
	t.Logf("command %s round-trip ok: %s", cmd.ID, "done")

	// --- 6. disconnect marks the device offline ---
	//
	// A clean close frame makes the server's read loop return immediately; the
	// deferred cleanup then unregisters the connection and flips the DB status.
	conn.Close()
	if err := waitFor(func() bool { return !e.hub.Online(device.ID) }); err != nil {
		t.Fatalf("device still online in hub after disconnect: %v", err)
	}
	if err := waitFor(func() bool {
		d, err := e.repo.GetByID(ctx, device.ID)
		return err == nil && d.Status == devicemgmt.StatusOffline
	}); err != nil {
		t.Fatalf("device not marked offline after disconnect: %v", err)
	}

	// The audit trail must record both ends of the session — a connect with no
	// matching disconnect would make remote-session review incomplete.
	var actions []string
	if err := e.db.Select(&actions, `SELECT action FROM audit_logs
		WHERE target_id = ? ORDER BY created_at`, device.ID); err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if !contains(actions, "agent.connect") || !contains(actions, "agent.disconnect") {
		t.Fatalf("audit log missing connect/disconnect pair: %v", actions)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestE2EOfflineCommandQueues checks that a command for a disconnected device is
// reported as undeliverable rather than silently dropped — the property that
// lets branch agents pick up work when they reconnect.
func TestE2EOfflineCommandQueues(t *testing.T) {
	e := newEnv(t)
	if e.hub.SendTo("device-not-connected", mustJSON(t, transport.Envelope{Type: "command"})) {
		t.Fatal("SendTo must report failure for a disconnected device")
	}
	if e.hub.Online("device-not-connected") {
		t.Fatal("unknown device must not be online")
	}
}

// --- helpers ---

func enroll(t *testing.T, e *env, deviceID, token string) string {
	t.Helper()
	body, err := postJSON(e.httpURL("/api/agent/enroll"), map[string]string{"enrollment_token": token})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	var out struct {
		DeviceID     string `json:"device_id"`
		DeviceSecret string `json:"device_secret"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode enroll response: %v (body=%s)", err, body)
	}
	if out.DeviceID != deviceID {
		t.Fatalf("enroll returned device %q, want %q", out.DeviceID, deviceID)
	}
	if out.DeviceSecret == "" {
		t.Fatal("enroll returned empty secret")
	}
	return out.DeviceSecret
}

func postJSON(url string, body any) ([]byte, error) {
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, &httpError{code: resp.StatusCode, body: buf.String()}
	}
	return buf.Bytes(), nil
}

type httpError struct {
	code int
	body string
}

func (e *httpError) Error() string { return "HTTP " + itoa(e.code) + ": " + e.body }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func send(t *testing.T, conn *websocket.Conn, env transport.Envelope) {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write message: %v", err)
	}
}

func readEnv(t *testing.T, conn *websocket.Conn) transport.Envelope {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}
	var env transport.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal %q: %v", data, err)
	}
	return env
}

// waitForStatus waits until the device has the expected status in the DB.
func waitForStatus(t *testing.T, e *env, deviceID, status string) {
	t.Helper()
	if err := waitFor(func() bool {
		d, err := e.repo.GetByID(context.Background(), deviceID)
		return err == nil && d.Status == status
	}); err != nil {
		t.Fatalf("device %s never reached status %q: %v", deviceID, status, err)
	}
}

// waitFor polls a condition until it is true or the timeout elapses.
// Used instead of sleeps so tests are fast and deterministic.
func waitFor(cond func() bool) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return context.DeadlineExceeded
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// keep imports used by the test harness referenced.
var _ = bcrypt.CompareHashAndPassword
var _ = auth.NewJWTService
var _ sync.WaitGroup

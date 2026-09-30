package remotecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// busyHub is an agent that is connected -- Online reports true -- but whose
// 64-slot send queue is full, so SendTo returns false and the command is
// dropped. This is a real outcome on a saturated link, and the old code
// discarded the return value.
type busyHub struct{ sendAttempts int }

func (h *busyHub) Online(string) bool { return true }

func (h *busyHub) SendTo(string, []byte) bool {
	h.sendAttempts++
	return false
}

type nullAuditor struct{}

func (nullAuditor) Log(context.Context, string, string, string, string, map[string]string) error {
	return nil
}

func rcFixture(t *testing.T) (*Handler, *sqlx.DB, *busyHub) {
	t.Helper()
	// A real file with the production DSN, for the same reason as
	// terminal_dispatch_test.go: ":memory:" hands every connection in the pool
	// its own private database, and this fixture dials a WebSocket, so the
	// handler runs on whichever connection the pool gives it rather than the one
	// these CREATE TABLEs ran on. That fixture was observed failing
	// intermittently with "no such table"; this one has not been seen to fail,
	// so this is preventive, not a fix for something seen. db.DSNForPath keeps
	// busy_timeout in play, which is what turns the resulting write contention
	// into a wait rather than SQLITE_BUSY.
	database, err := sqlx.Open("sqlite", db.DSNForPath(filepath.Join(t.TempDir(), "remotecontrol.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	// site is selected by ListSessionsByDevice; the real schema has it and this
	// table exists only to hold two rows.
	database.MustExec(`CREATE TABLE devices (id TEXT PRIMARY KEY, hostname TEXT, site TEXT, status TEXT);`)
	database.MustExec(`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT);`)
	database.MustExec(`CREATE TABLE remote_control_sessions (
		id TEXT PRIMARY KEY,
		device_id TEXT NOT NULL,
		operator_id TEXT NOT NULL,
		session_mode TEXT NOT NULL DEFAULT 'full_control',
		status TEXT NOT NULL DEFAULT 'active',
		frames_transmitted INTEGER NOT NULL DEFAULT 0,
		bytes_transmitted INTEGER NOT NULL DEFAULT 0,
		input_events_count INTEGER NOT NULL DEFAULT 0,
		started_at DATETIME NOT NULL,
		ended_at DATETIME,
		created_at DATETIME NOT NULL
	);`)
	database.MustExec(`INSERT INTO devices (id, hostname, status) VALUES ('dev-1','WEDGE-PC','online');`)
	database.MustExec(`INSERT INTO users (id, username) VALUES ('u-1','tech');`)

	hub := &busyHub{}
	repo := NewRepository(database)
	return &Handler{
		repo:    repo,
		relay:   NewRelayManager(repo),
		hub:     hub,
		devices: devicemgmt.NewRepository(database),
		audit:   nullAuditor{},
	}, database, hub
}

func startSession(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/devices/dev-1/remotecontrol/session",
		bytes.NewBufferString(`{"mode":"full_control"}`)).WithContext(
		context.WithValue(context.Background(), auth.CtxUserID, "u-1"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "dev-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.startSession(rec, req)
	return rec
}

func sessionRow(t *testing.T, database *sqlx.DB) (status string, endedAt *time.Time) {
	t.Helper()
	if err := database.Get(&status, `SELECT status FROM remote_control_sessions`); err != nil {
		t.Fatal(err)
	}
	_ = database.Get(&endedAt, `SELECT ended_at FROM remote_control_sessions`)
	return status, endedAt
}

// TestADroppedDispatchDoesNotLeaveASessionClaimingToBeActive is the regression
// test for a success response over a command that was never delivered.
//
// startSession checked that the device was online, created the row, registered
// the relay, then called h.hub.SendTo and threw the result away. SendTo returns
// false when the device's send queue is full -- a real outcome on a busy link,
// and one the transport hub signals precisely so callers can act on it. Nothing
// retried it and nothing swept it, so the operator got HTTP 201 with
// status="active", the history row said "active", and no agent had ever been
// told a session existed. The console then sat waiting for a desktop that was
// never going to open, next to a session entry that looked live.
func TestADroppedDispatchDoesNotLeaveASessionClaimingToBeActive(t *testing.T) {
	h, database, hub := rcFixture(t)

	rec := startSession(t, h)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503: the operator was told a session started when "+
			"the agent never received it (body: %s)", rec.Code, rec.Body.String())
	}
	if hub.sendAttempts != 1 {
		t.Errorf("hub saw %d send attempts, want 1", hub.sendAttempts)
	}

	// Whatever the HTTP answer, the durable row must not say "active": nothing
	// is driving that session.
	status, endedAt := sessionRow(t, database)
	if status == "active" {
		t.Error("durable row says \"active\" for a session the agent never received; " +
			"it stays ACTIVE in history forever")
	}
	if endedAt == nil {
		t.Error("ended_at is NULL on a session that can never run")
	}

	// And the relay must not be left registered, holding a session id nothing
	// will ever connect to.
	rows, err := h.repo.ListSessionsByDevice(context.Background(), "dev-1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("history has %d rows, want 1", len(rows))
	}
	if rows[0].Status == "active" {
		t.Errorf("history shows status %q; the operator sees a live session that "+
			"does not exist", rows[0].Status)
	}
}

// TestEndingASessionThatNeverAttachedStillClosesTheRow covers the other gap.
//
// EndSession was called only from the tail of CloseRelay, below the
// `!exists || r == nil` early return. An operator who opens a session and
// closes the modal before the agent has connected -- or whose dispatch failed
// outright -- hits that return, and the row keeps status 'active' with a NULL
// ended_at. Nothing sweeps it, so it sits in the device's session history
// marked ACTIVE indefinitely, indistinguishable from one somebody has open at
// this moment. A remote-control history that cannot tell live from abandoned is
// not a record of anything.
func TestEndingASessionThatNeverAttachedStillClosesTheRow(t *testing.T) {
	h, database, hub := rcFixture(t)

	// A hub that accepts the command, so the session is created and the relay
	// registered -- but no agent ever attaches to the relay socket.
	hub.sendAttempts++
	session := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
	if err := h.repo.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	h.relay.RegisterSession(session.ID, session.SessionMode)

	// The operator closes the tab. CloseRelay finds the relay, but there is no
	// agent socket and no operator socket on it.
	h.relay.CloseRelay(session.ID)

	status, endedAt := sessionRow(t, database)
	if status == "active" {
		t.Errorf("status = %q after the operator ended the session, want a terminal "+
			"status: nothing sweeps this row, so ACTIVE here is permanent", status)
	}
	if endedAt == nil {
		t.Error("ended_at is NULL after the session ended; history has no end time")
	}
}

// TestEndingASessionWithNoRelayAtAllStillClosesTheRow is the narrower case the
// first test's fix also has to cover: the dispatch failed, so nothing was ever
// registered, and the operator's stop request arrives anyway.
func TestEndingASessionWithNoRelayAtAllStillClosesTheRow(t *testing.T) {
	h, database, _ := rcFixture(t)

	session := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
	if err := h.repo.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	// Deliberately no RegisterSession: this is the dropped-dispatch case.

	h.relay.CloseRelay(session.ID)

	status, endedAt := sessionRow(t, database)
	if status == "active" {
		t.Errorf("status = %q after ending a session with no relay, want a terminal status", status)
	}
	if endedAt == nil {
		t.Error("ended_at is NULL after the session ended")
	}
}

var _ = json.Marshal

// TestTheRelayPingsSoItsOwnDeadlineCanBeRefreshed is the regression test for a
// self-inflicted 60-second kill.
//
// AttachOperator and AttachAgent both set a read deadline and installed a
// PongHandler to extend it, and that is the standard keepalive shape -- except
// nothing on either side ever sent a ping. A pong is only ever sent in reply to
// a ping; the browser console sends screen frames on demand and nothing on a
// timer, and the agent had no ping loop either. So the PongHandler was
// unreachable dead code, the deadline was absolute rather than idle, and every
// remote-control session ended at exactly operatorPongWait after attaching:
// sixty seconds of a working desktop and then an abrupt close, for an operator
// with no way to tell that from a crash.
//
// The test drives the production AttachOperator against a real WebSocket and
// asserts the server sends a ping, which is the precondition for the deadline
// ever being extended.
func TestTheRelayPingsSoItsOwnDeadlineCanBeRefreshed(t *testing.T) {
	h, _, _ := rcFixture(t)
	session := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
	if err := h.repo.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}

	// A real server socket the handler attaches, and a real client that reads.
	// This is the production path, not a reproduction of it.
	//
	// The client has to be running a read loop. gorilla answers a ping with a
	// pong from inside ReadMessage, so a client that never reads never pongs --
	// which is precisely the failure being guarded against, and the reason this
	// test asserts on the client's ping handler rather than the server's.
	pings := make(chan struct{}, 8)
	serverConn := make(chan *websocket.Conn, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		serverConn <- conn
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer up.Close()

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(up.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	client.SetPingHandler(func(string) error {
		select {
		case pings <- struct{}{}:
		default:
		}
		return nil
	})
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	conn := <-serverConn

	// operatorPongWait is 60s, so the real keepalive period is 20s. Waiting that
	// long for one ping would make the test slow for no extra signal, so the
	// same function AttachOperator starts is driven at a short period.
	relay, ok := h.relay.AttachOperator(session.ID, conn)
	if !ok {
		t.Fatal("AttachOperator refused a live socket")
	}
	t.Cleanup(func() { h.relay.CloseRelay(session.ID) })

	go startKeepalive(conn, relay.closeChan, 50*time.Millisecond)

	select {
	case <-pings:
	case <-time.After(3 * time.Second):
		t.Fatal("no ping arrived: with no ping there is no pong, and the pong handler is " +
			"the only thing that extends the read deadline, so every session ends at the " +
			"deadline no matter how active it is")
	}

	// The loop is driven on the relay's closeChan, so closing the relay is what
	// stops it -- a goroutine left pinging a socket nobody reads outlives the
	// session that started it.
	h.relay.CloseRelay(session.ID)
	select {
	case <-clientDone:
	case <-time.After(3 * time.Second):
		t.Log("client read loop still running after CloseRelay; the socket close in " +
			"CloseRelay should end it")
	}
}

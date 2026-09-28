package remoteexec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// wedgedHub is a device that is connected -- Online reports true -- but whose
// send queue is full, so SendTo returns false and the command never leaves the
// server.
//
// The two answers are deliberately different, and the difference is the whole
// point. A hub that is offline takes the branch above the upgrade and the
// operator gets a clean 409; that path was already correct. The bug needs a
// device that hub.Online calls online while SendTo refuses, which is what a
// saturated link or a wedged agent whose write pump has stopped draining looks
// like. Collapsing the two into one flag is what made the first draft of this
// test fail its own setup: it never reached the send it was meant to guard.
type wedgedHub struct{ online, accepts bool }

func (h *wedgedHub) Online(string) bool { return h.online }

func (h *wedgedHub) SendTo(string, []byte) bool { return h.accepts }

type recordingAudit struct{ actions []string }

func (a *recordingAudit) Log(_ context.Context, _, _, action, _ string, _ map[string]string) error {
	a.actions = append(a.actions, action)
	return nil
}

type staticRoleReader struct{}

func (staticRoleReader) RoleFor(context.Context, string) (string, string, error) {
	return "tech", rbac.RoleTechnician, nil
}

// terminalFixture builds a handler over a real database holding one device, a
// hub that is online, and an access token for the handshake. accepts controls
// only whether the device's queue takes the command; the device is always
// online, so the branch under test is always the one after the offline gate.
func terminalFixture(t *testing.T, accepts bool) (*Handler, *sqlx.DB, *recordingAudit, string) {
	t.Helper()

	database, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	// hostname and operator_name are selected by ListTerminalSessions; the real
	// schema has them and these tables exist only to hold the rows under test.
	database.MustExec(`CREATE TABLE devices (id TEXT PRIMARY KEY, hostname TEXT);`)
	database.MustExec(`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT);`)
	database.MustExec(`CREATE TABLE terminal_sessions (
		id TEXT PRIMARY KEY,
		device_id TEXT NOT NULL,
		operator_id TEXT NOT NULL,
		shell_type TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		created_at DATETIME NOT NULL,
		closed_at DATETIME
	);`)
	database.MustExec(`INSERT INTO devices (id, hostname) VALUES ('dev-1','WEDGE-PC');`)
	database.MustExec(`INSERT INTO users (id, username) VALUES ('u-1','tech');`)

	// The ?token= path is exercised rather than the ticket path because it needs
	// only a JWT service, whereas the ticket path reads package-level state in
	// auth that a parallel test could also be setting. Both reach the same
	// authenticateHandshake and the same send.
	jwtSvc := auth.NewJWTService("test-secret-for-terminal-dispatch-only", time.Hour, 24*time.Hour)
	tokenPair, err := jwtSvc.Issue("u-1", "tech", rbac.RoleTechnician)
	if err != nil {
		t.Fatal(err)
	}

	hub := &wedgedHub{online: true, accepts: accepts}
	auditor := &recordingAudit{}
	h := &Handler{
		repo:    NewRepository(database),
		relay:   NewTerminalRelay(),
		hub:     hub,
		jwtSvc:  jwtSvc,
		devices: devicemgmt.NewRepository(database),
		audit:   auditor,
		// The test drives the production path: a real browser WebSocket against
		// a real chi route.
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
	}

	return h, database, auditor, tokenPair.AccessToken
}

// dialTerminal opens the operator's half of the terminal socket against the
// production handler, and returns the frames the server pushes to the browser.
func dialTerminal(t *testing.T, h *Handler, accessToken string) <-chan string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "dev-1")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		h.handleTerminalWS(w, r)
	}))
	t.Cleanup(srv.Close)

	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"?token="+accessToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	frames := make(chan string, 8)
	go func() {
		defer close(frames)
		for {
			typ, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if typ != websocket.TextMessage {
				continue
			}
			var frame struct {
				Type string `json:"type"`
				Data string `json:"data"`
			}
			if err := json.Unmarshal(payload, &frame); err != nil {
				continue
			}
			select {
			case frames <- frame.Type + ": " + frame.Data:
			default:
			}
		}
	}()
	return frames
}

func firstFrame(t *testing.T, frames <-chan string) string {
	t.Helper()
	select {
	case f, ok := <-frames:
		if !ok {
			t.Fatal("the server socket closed without sending a single frame")
		}
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("no frame arrived within 5s")
		return ""
	}
}

func sessionRow(t *testing.T, database *sqlx.DB) (status string, closedAt *time.Time) {
	t.Helper()
	if err := database.Get(&status, `SELECT status FROM terminal_sessions`); err != nil {
		t.Fatal(err)
	}
	if err := database.Get(&closedAt, `SELECT closed_at FROM terminal_sessions`); err != nil {
		t.Fatal(err)
	}
	return status, closedAt
}

// TestADroppedTerminalOpenIsNotAnnouncedAsAShell is the regression test for a
// session the agent never received being reported to the operator as a live
// prompt.
//
// handleTerminalWS checked that the device was online, upgraded the browser
// socket, wrote the terminal_sessions row, registered the relay, and then
// marshalled term.open and called h.hub.SendTo -- discarding the return value.
// SendTo comes back false when the device's send queue is full, which is a
// thing the hub signals precisely so callers can act on it. The handler then
// wrote "Terminal session %s established" to the browser and sat in a read loop.
//
// So the operator got a confident "established" and a prompt, and every
// keystroke was forwarded into the same full queue and dropped with the same
// result. Nothing retried it: once the agent's queue drained, term.open was
// already gone. Nothing swept it either, so terminal_sessions kept status
// 'active' for a shell that had never started.
func TestADroppedTerminalOpenIsNotAnnouncedAsAShell(t *testing.T) {
	h, database, auditor, token := terminalFixture(t, false)

	frames := dialTerminal(t, h, token)
	first := firstFrame(t, frames)

	if strings.Contains(first, "established") {
		t.Fatalf("browser was told the session was established (frame: %q) when the "+
			"agent never received term.open", first)
	}

	status, closedAt := sessionRow(t, database)
	if status == "active" {
		t.Error("durable row says \"active\" for a terminal that never started; it " +
			"stays ACTIVE in history forever")
	}
	if closedAt == nil {
		t.Error("closed_at is NULL on a session that can never run")
	}

	var sawFailure bool
	for _, a := range auditor.actions {
		if a == "terminal.open_failed" {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Errorf("audit actions = %v, want a terminal.open_failed entry: the drop is "+
			"invisible in the audit trail, which records only the successful open",
			auditor.actions)
	}
}

// TestTheBrowserIsToldWhyTheTerminalRefused: the socket is already upgraded by
// the time the send fails, so an HTTP status is no longer available and the
// refusal has to travel over the WebSocket. A silent close is what the operator
// actually experienced, and it is indistinguishable from a network drop.
func TestTheBrowserIsToldWhyTheTerminalRefused(t *testing.T) {
	h, _, _, token := terminalFixture(t, false)

	first := firstFrame(t, dialTerminal(t, h, token))
	if !strings.HasPrefix(first, "term.error") {
		t.Errorf("first frame = %q, want a term.error frame: the browser socket "+
			"closes with no explanation otherwise", first)
	}
}

// TestATerminalThatIsAcceptedStillOpens: the guard above must not have cost the
// working path. A hub that accepts the command produces the established message
// and leaves the row active for the deferred cleanup to close.
func TestATerminalThatIsAcceptedStillOpens(t *testing.T) {
	h, database, _, token := terminalFixture(t, true)

	first := firstFrame(t, dialTerminal(t, h, token))
	if !strings.Contains(first, "established") {
		t.Fatalf("first frame = %q, want the established confirmation", first)
	}

	// The row is still active at this point: the handler is parked in its read
	// loop waiting for the operator, which is the correct live state.
	status, closedAt := sessionRow(t, database)
	if status != "active" {
		t.Errorf("status = %q while the session is live, want \"active\"", status)
	}
	if closedAt != nil {
		t.Error("closed_at is set while the session is live")
	}
}

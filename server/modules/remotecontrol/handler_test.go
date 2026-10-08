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
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/wsticket"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// acceptingHub is the ordinary case: the device is online and its queue takes
// the command. rcFixture's busyHub can only ever refuse, so the successful path
// through startSession is otherwise never driven.
type acceptingHub struct{ sends int }

func (h *acceptingHub) Online(string) bool { return true }

func (h *acceptingHub) SendTo(string, []byte) bool {
	h.sends++
	return true
}

// offlineHub is a device that is not connected at all.
type offlineHub struct{}

func (offlineHub) Online(string) bool         { return false }
func (offlineHub) SendTo(string, []byte) bool { return true }

// captureHub keeps the bytes it was handed, so a test can assert on what the
// agent is actually told rather than only on the HTTP response.
type captureHub struct {
	online  bool
	payload []byte
}

func (c *captureHub) Online(string) bool { return c.online }

func (c *captureHub) SendTo(_ string, msg []byte) bool {
	c.payload = msg
	return true
}

// rcRouteFixture is rcFixture plus the devices table columns the agent-facing
// endpoint authenticates against, and a token issuer for the operator socket.
//
// devices carries capabilities because a device row here is the one
// devicemgmt.Repository.GetByID scans, and it uses SELECT *: a column the
// fixture omits is simply absent from the scan and no error is raised, so a
// missing column shows up as a zero value rather than a failure.
func rcRouteFixture(t *testing.T) (*Handler, *sqlx.DB, *devicemgmt.Repository) {
	t.Helper()

	database, err := sqlx.Open("sqlite", db.DSNForPath(filepath.Join(t.TempDir(), "rchandler.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC()
	mustExecDevices(t, database)
	database.MustExec(`INSERT INTO devices
		(id, hostname, site, status, os_name, os_version, agent_version, enrolled_at,
		 enrollment_token_hash, device_secret_hash, capabilities, created_at, updated_at)
		VALUES ('dev-1','WEDGE-PC','HQ','online','windows','11','1.0.0',?,NULL,?,'[]',?,?)`,
		now, devicemgmt.HashToken("dev-1-secret"), now, now)
	database.MustExec(`INSERT INTO devices
		(id, hostname, site, status, os_name, os_version, agent_version, enrolled_at,
		 enrollment_token_hash, device_secret_hash, capabilities, created_at, updated_at)
		VALUES ('dev-2','OTHER-PC','HQ','online','windows','11','1.0.0',?,NULL,?,'[]',?,?)`,
		now, devicemgmt.HashToken("dev-2-secret"), now, now)
	database.MustExec(`INSERT INTO users (id, username) VALUES ('u-1','tech');`)

	repo := NewRepository(database)
	return &Handler{
		repo:    repo,
		relay:   NewRelayManager(repo),
		hub:     &acceptingHub{},
		devices: devicemgmt.NewRepository(database),
		audit:   nullAuditor{},
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}, database, devicemgmt.NewRepository(database)
}

func mustExecDevices(t *testing.T, database *sqlx.DB) {
	t.Helper()
	database.MustExec(`CREATE TABLE devices (
		id TEXT PRIMARY KEY,
		hostname TEXT NOT NULL DEFAULT '',
		site TEXT,
		status TEXT NOT NULL DEFAULT 'online',
		os_name TEXT NOT NULL DEFAULT '',
		os_version TEXT,
		agent_version TEXT,
		last_seen_at DATETIME,
		enrolled_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		enrollment_token_hash TEXT,
		device_secret_hash TEXT NOT NULL DEFAULT '',
		retired_at DATETIME,
		capabilities TEXT,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`)
	database.MustExec(`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL);`)
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
}

// issueTicket mints the one-time ticket the operator socket redeems, wiring the
// store the way main.go does. The handshake accepts nothing else, so a test that
// wants to reach the relay has to come through this path.
//
// The role is the live one the wired reader reports. The fixture's users table
// carries no role column, so the reader here is the test's own and the role
// asked for is the one it answers with.
func issueTicket(t *testing.T, h *Handler, userID, role string) string {
	t.Helper()

	ticketDB, err := db.Open(filepath.Join(t.TempDir(), "rc-tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ticketDB.Close() })

	store := wsticket.NewStore(ticketDB, 0)
	auth.SetWebSocketTicketIssuer(store, stubRoles{userID: role})
	t.Cleanup(func() { auth.SetWebSocketTicketIssuer(nil, nil) })

	ticket, err := store.Issue(context.Background(), userID, auth.PurposeRemoteDesktop, wsticket.DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

// stubRoles answers the role lookup a redeemed ticket triggers. It reports the
// role the test asked for rather than reading a column the fixture's users
// table does not have.
type stubRoles map[string]string

func (r stubRoles) RoleFor(_ context.Context, userID string) (string, string, error) {
	role, ok := r[userID]
	if !ok {
		return "", "", auth.ErrUserNotActive
	}
	return userID, role, nil
}

// startSessionRequest drives startSession over a real route so the chi URL param
// and the user id in the context both come from the request, not from a hand
// built context.
func startSessionRequest(t *testing.T, h *Handler, deviceID, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Post("/api/devices/{id}/remotecontrol/session", h.startSession)

	req := httptest.NewRequest(http.MethodPost,
		"/api/devices/"+deviceID+"/remotecontrol/session", bytes.NewBufferString(body))
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "u-1"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestStartSessionRefusesAnUnknownOrOfflineDevice: remote control puts a live
// desktop on an operator's screen. Creating a session for a machine that does
// not exist, or one that is not connected, produces a 201 over nothing and an
// entry in the operator's history that never opens.
func TestStartSessionRefusesAnUnknownOrOfflineDevice(t *testing.T) {
	cases := []struct {
		name   string
		device string
		hub    Hub
		want   int
	}{
		{"unknown device", "no-such-device", &acceptingHub{}, http.StatusNotFound},
		{"device offline", "dev-1", offlineHub{}, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, database, _ := rcRouteFixture(t)
			h.hub = tc.hub

			rec := startSessionRequest(t, h, tc.device, `{"mode":"full_control"}`)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d\nbody: %s", rec.Code, tc.want, rec.Body.String())
			}
			var n int
			if err := database.Get(&n, `SELECT COUNT(*) FROM remote_control_sessions`); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("session rows = %d, want 0: a refused start must not leave a row", n)
			}
		})
	}
}

// TestStartSessionRefusesAModeThatDoesNotExist: the mode is the gate on whether
// the operator's keystrokes reach the endpoint, so a typo that silently became
// full_control would be an input-injection hole, not a cosmetic problem.
func TestStartSessionRefusesAModeThatDoesNotExist(t *testing.T) {
	h, _, _ := rcRouteFixture(t)

	rec := startSessionRequest(t, h, "dev-1", `{"mode":"anything_goes"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}
}

// TestStartSessionAcceptsBothModesAndTellsTheAgentWhichOne: an empty body means
// full_control, and the relay_url the agent receives has to name this device's
// own agent socket. A relay_url built from the wrong device would attach one
// machine's screen stream to another machine's session.
func TestStartSessionAcceptsBothModesAndTellsTheAgentWhichOne(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantMode string
		wantCmd  string
	}{
		{"default mode", ``, "full_control", "rc.start"},
		{"view only", `{"mode":"view_only"}`, "view_only", "rc.start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := rcRouteFixture(t)
			hub := &captureHub{online: true}
			h.hub = hub

			rec := startSessionRequest(t, h, "dev-1", tc.body)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
			}

			var session RemoteControlSession
			if err := json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
				t.Fatal(err)
			}
			if session.SessionMode != tc.wantMode {
				t.Errorf("session_mode = %q, want %q", session.SessionMode, tc.wantMode)
			}
			if session.OperatorID != "u-1" {
				t.Errorf("operator_id = %q, want \"u-1\": a session has to name who opened it",
					session.OperatorID)
			}

			if hub.payload == nil {
				t.Fatal("nothing was sent to the agent")
			}
			var env struct {
				Command string `json:"command"`
				Payload struct {
					SessionID string `json:"session_id"`
					Mode      string `json:"mode"`
					RelayURL  string `json:"relay_url"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(hub.payload, &env); err != nil {
				t.Fatal(err)
			}
			if env.Command != tc.wantCmd {
				t.Errorf("command = %q, want %q", env.Command, tc.wantCmd)
			}
			if env.Payload.SessionID != session.ID {
				t.Errorf("agent was told session %q, the response says %q: the agent "+
					"would attach to a relay the operator is not on",
					env.Payload.SessionID, session.ID)
			}
			if env.Payload.Mode != tc.wantMode {
				t.Errorf("agent was told mode %q, the session is %q",
					env.Payload.Mode, tc.wantMode)
			}
			want := "/api/agent/devices/dev-1/remotecontrol/ws?session=" + session.ID
			if env.Payload.RelayURL != want {
				t.Errorf("relay_url = %q, want %q", env.Payload.RelayURL, want)
			}

			// And the relay is registered under that same id, or the agent has
			// somewhere to connect but the operator does not.
			if relay := h.relay.GetRelay(session.ID); relay == nil {
				t.Error("no relay is registered for the session the agent was told to open")
			} else if relay.CurrentMode() != tc.wantMode {
				t.Errorf("relay mode = %q, want %q", relay.CurrentMode(), tc.wantMode)
			}
		})
	}
}

// TestStoppingASessionIsRecordedAsEnded: stopSession only touched the relay. The
// durable row is what the console's session history reads, so a session that
// was stopped must not keep showing ACTIVE there.
func TestStoppingASessionIsRecordedAsEnded(t *testing.T) {
	h, database, _ := rcRouteFixture(t)
	hub := &acceptingHub{}
	h.hub = hub

	session := &RemoteControlSession{
		DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control",
	}
	if err := h.repo.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	h.relay.RegisterSession(session.ID, session.SessionMode)

	r := chi.NewRouter()
	r.Post("/api/devices/{id}/remotecontrol/sessions/{sessionId}/stop", h.stopSession)
	req := httptest.NewRequest(http.MethodPost,
		"/api/devices/dev-1/remotecontrol/sessions/"+session.ID+"/stop", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "u-1"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var status string
	var endedAt *time.Time
	if err := database.Get(&status,
		`SELECT status FROM remote_control_sessions WHERE id = ?`, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.Get(&endedAt,
		`SELECT ended_at FROM remote_control_sessions WHERE id = ?`, session.ID); err != nil {
		t.Fatal(err)
	}
	if status == "active" {
		t.Errorf("status = %q after the operator stopped the session", status)
	}
	if endedAt == nil {
		t.Error("ended_at is NULL on a stopped session; history has no end time")
	}
}

// TestTheSessionListReadsAsAnArrayAndHonoursItsLimit: the console iterates this
// body without a null check, and the limit is what stops one device's history
// from being read in full on every page load.
func TestTheSessionListReadsAsAnArrayAndHonoursItsLimit(t *testing.T) {
	h, _, _ := rcRouteFixture(t)

	list := func(query string) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Get("/api/devices/{id}/remotecontrol/sessions", h.listSessions)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/devices/dev-1/remotecontrol/sessions"+query, nil))
		return rec
	}

	rec := list("")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("empty list body = %s, want []", got)
	}

	for i := 0; i < 4; i++ {
		session := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
		if err := h.repo.CreateSession(context.Background(), session); err != nil {
			t.Fatal(err)
		}
	}

	rec = list("?limit=2")
	var sessions []RemoteControlSession
	if err := json.Unmarshal(rec.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Errorf("limit=2 returned %d sessions", len(sessions))
	}

	// A junk limit falls back to the default rather than reading everything.
	rec = list("?limit=not-a-number")
	sessions = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 4 {
		t.Errorf("a junk limit returned %d sessions, want all 4 via the default", len(sessions))
	}
}

// TestAViewerCannotOpenTheOperatorSocket covers the role gate on the WebSocket
// path, which does not go through rbac.RequireRole and so has to check the role
// itself.
func TestAViewerCannotOpenTheOperatorSocket(t *testing.T) {
	h, _, _ := rcRouteFixture(t)
	hub := &acceptingHub{}
	h.hub = hub

	ticket := issueTicket(t, h, "u-1", "viewer")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "dev-1")
		h.handleOperatorWS(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
	}))
	defer srv.Close()

	_, resp, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/?session=s1&ticket="+ticket, nil)
	if err == nil {
		t.Fatal("a viewer opened the remote control socket")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}

// TestAnOperatorSocketWithNoCredentialIsRefused, and the replayed-ticket case:
// a ticket is single-use, so a second attempt with the same value is the
// signature of a credential copied out of an access log.
func TestAnOperatorSocketWithNoCredentialIsRefused(t *testing.T) {
	h, _, _ := rcRouteFixture(t)
	hub := &acceptingHub{}
	h.hub = hub

	ticket := issueTicket(t, h, "u-1", "tech")
	_ = h.repo.CreateSession(context.Background(), &RemoteControlSession{
		ID:          "spent",
		DeviceID:    "dev-1",
		OperatorID:  "u-1",
		SessionMode: "full_control",
	})

	// Spend the ticket first, the way the real handshake does. A ticket that was
	// never presented is not a replay -- it is an outstanding ticket, and
	// refusing it would mean the store rejects valid credentials. The replay
	// case is the second attempt with a value an access log already recorded.
	srv0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "dev-1")
		h.handleOperatorWS(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
	}))
	conn0, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv0.URL, "http")+"/?session=spent&ticket="+ticket, nil)
	if err != nil {
		t.Fatalf("the first use of the ticket was refused: %v", err)
	}
	_ = conn0.Close()
	srv0.Close()

	cases := []struct {
		name  string
		query string
	}{
		{"no credential at all", "session=s1"},
		{"no session", "ticket=" + ticket},
		{"forged ticket", "session=s1&ticket=not-a-real-ticket"},
		{"a spent ticket replayed", "session=s1&ticket=" + ticket},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rctx := chi.NewRouteContext()
				rctx.URLParams.Add("id", "dev-1")
				h.handleOperatorWS(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
			}))
			defer srv.Close()

			_, resp, err := websocket.DefaultDialer.Dial(
				"ws"+strings.TrimPrefix(srv.URL, "http")+"/?"+tc.query, nil)
			if err == nil {
				t.Fatal("the socket opened without a usable credential")
			}
			if resp == nil {
				t.Fatal("no response to inspect")
			}
			if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 or 403", resp.StatusCode)
			}
		})
	}
}

// TestTheAgentCannotAttachToAnotherDevicesSession is the ownership check on the
// agent-facing socket.
//
// handleAgentWS authenticated the device by its own secret, which proves which
// agent is calling but says nothing about which session it may join. Without the
// comparison against session.DeviceID, any enrolled device could attach to
// another device's session by guessing or leaking a session id and push screen
// frames into an operator's view of a third machine.
func TestTheAgentCannotAttachToAnotherDevicesSession(t *testing.T) {
	h, _, _ := rcRouteFixture(t)
	hub := &acceptingHub{}
	h.hub = hub

	session := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
	if err := h.repo.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	h.relay.RegisterSession(session.ID, session.SessionMode)

	// dev-2 is enrolled and authenticated with its own valid secret. It does not
	// own this session.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "dev-1")
		h.handleAgentWS(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
	}))
	defer srv.Close()

	_, resp, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/?session="+session.ID, dev1Headers("dev-2", "dev-2-secret"))
	if err == nil {
		t.Fatal("a device attached to a session it does not own")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}

	if relay := h.relay.GetRelay(session.ID); relay == nil {
		t.Fatal("the relay is gone")
	}
}

// TestTheOwningDeviceStillAttaches: the check above must not have cost the
// working path, or no remote control session would ever open.
func TestTheOwningDeviceStillAttaches(t *testing.T) {
	h, _, _ := rcRouteFixture(t)
	hub := &acceptingHub{}
	h.hub = hub

	session := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
	if err := h.repo.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	h.relay.RegisterSession(session.ID, session.SessionMode)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "dev-1")
		h.handleAgentWS(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/?session="+session.ID, dev1Headers("dev-1", "dev-1-secret"))
	if err != nil {
		t.Fatalf("the owning device was refused: %v", err)
	}
	defer conn.Close()

	// The relay has to have an agent socket on it, or frames go nowhere.
	deadline := time.Now().Add(3 * time.Second)
	for {
		relay := h.relay.GetRelay(session.ID)
		if relay != nil {
			relay.mu.Lock()
			attached := relay.agentWS != nil
			relay.mu.Unlock()
			if attached {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the owning device's socket never attached to the relay")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestOperatorCannotConnectToAnotherOperatorsSession verifies that a session
// created by u-1 cannot be connected to by u-2 (technician), but can be accessed
// by an admin.
func TestOperatorCannotConnectToAnotherOperatorsSession(t *testing.T) {
	h, database, _ := rcRouteFixture(t)
	h.hub = &acceptingHub{}
	database.MustExec(`INSERT INTO users (id, username) VALUES ('u-2','other-tech');`)

	session := &RemoteControlSession{ID: "s-owner-1", DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
	if err := h.repo.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	h.relay.RegisterSession(session.ID, session.SessionMode)

	ticketUser2 := issueTicket(t, h, "u-2", "tech")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "dev-1")
		h.handleOperatorWS(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
	}))
	defer srv.Close()

	// Technician u-2 tries to connect to u-1's session -> must fail 403
	_, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/?session="+session.ID+"&ticket="+ticketUser2, nil)
	if err == nil {
		t.Fatal("expected 403 Forbidden when technician u-2 connects to u-1's session, but dial succeeded")
	}

	// Admin user can connect to u-1's session
	database.MustExec(`INSERT INTO users (id, username) VALUES ('u-admin','admin-user');`)
	ticketAdmin := issueTicket(t, h, "u-admin", "admin")
	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/?session="+session.ID+"&ticket="+ticketAdmin, nil)
	if err != nil {
		t.Fatalf("expected admin to be able to connect to session, but got: %v", err)
	}
	_ = conn.Close()
}
// it likes. One bad frame must not end a live desktop.
func TestAMalformedFrameFromTheOperatorIsIgnoredNotFatal(t *testing.T) {
	h, _, _ := rcRouteFixture(t)
	hub := &acceptingHub{}
	h.hub = hub

	session := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
	if err := h.repo.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	h.relay.RegisterSession(session.ID, session.SessionMode)

	ticket := issueTicket(t, h, "u-1", "tech")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "dev-1")
		h.handleOperatorWS(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/?session="+session.ID+"&ticket="+ticket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.WriteMessage(websocket.TextMessage, []byte(`{"type":`)) // truncated
	conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"input","data":"q"}`))

	// A frame the relay answers is proof the loop is still running.
	if err := conn.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"mode","mode":"view_only"}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if relay := h.relay.GetRelay(session.ID); relay != nil && relay.CurrentMode() == "view_only" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the operator read loop stopped at the malformed frame")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// parseModeMessage decides whether a console frame is a mode change or operator
// input. Getting it wrong in the second direction would either drop real
// keystrokes or let a keystroke masquerade as a mode change, so the boundaries
// are worth pinning.
func TestParseModeMessageRecognisesOnlyRealModeChanges(t *testing.T) {
	cases := []struct {
		name     string
		msg      string
		wantMode string
		wantOK   bool
	}{
		{"full control", `{"type":"mode","mode":"full_control"}`, "full_control", true},
		{"view only", `{"type":"mode","mode":"view_only"}`, "view_only", true},
		{"another type", `{"type":"input","mode":"view_only"}`, "", false},
		{"a mode outside the pair", `{"type":"mode","mode":"take_over"}`, "", false},
		{"not json", `mode=view_only`, "", false},
		{"empty", ``, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, ok := parseModeMessage([]byte(tc.msg))
			if ok != tc.wantOK || mode != tc.wantMode {
				t.Errorf("parseModeMessage(%q) = (%q, %t), want (%q, %t)",
					tc.msg, mode, ok, tc.wantMode, tc.wantOK)
			}
		})
	}
}

// dev1Headers is misnamed for the caller that uses it with dev-2, so it is
// named for what it does: attach a device's credentials to the upgrade request.
func dev1Headers(id, secret string) http.Header {
	h := http.Header{}
	h.Set("X-Device-Id", id)
	h.Set("X-Device-Secret", secret)
	return h
}

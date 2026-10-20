package remotecontrol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"
)

type rowState struct {
	Status  string     `db:"status"`
	Frames  int        `db:"frames_transmitted"`
	Bytes   int64      `db:"bytes_transmitted"`
	Inputs  int        `db:"input_events_count"`
	EndedAt *time.Time `db:"ended_at"`
}

func readRow(t *testing.T, database *sqlx.DB, id string) rowState {
	t.Helper()
	var row rowState
	if err := database.Get(&row, `SELECT status, frames_transmitted, bytes_transmitted,
		input_events_count, ended_at FROM remote_control_sessions WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	return row
}

// serverSocket returns the server side of a live WebSocket, for the relay to
// attach. The client side keeps reading so pings are answered.
func serverSocket(t *testing.T) *websocket.Conn {
	t.Helper()
	conns := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conns <- conn
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	go func() {
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()
	conn := <-conns
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestALateAgentDialAfterStopDoesNotReviveTheSession is the orphan relay.
//
// The worker can be mid-dial when rc.stop lands. handleAgentWS checked the
// device owned the session but not that the session was still active, and
// AttachAgent recreated the missing relay -- so a stopped session came back with
// a live relay, and when that relay closed, EndSession wrote its own counters
// and ended_at over the row the real close had already finished.
func TestALateAgentDialAfterStopDoesNotReviveTheSession(t *testing.T) {
	h, database, _ := rcRouteFixture(t)

	session := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
	if err := h.repo.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	relay := h.relay.RegisterSession(session.ID, session.SessionMode)
	for i := 0; i < 3; i++ {
		if err := relay.ForwardAgentFrame(websocket.BinaryMessage, []byte("frame")); err != nil {
			t.Fatal(err)
		}
	}
	h.relay.CloseRelay(session.ID) // the stop
	before := readRow(t, database, session.ID)
	if before.Status != "ended" || before.Frames != 3 {
		t.Fatalf("precondition: row = %+v, want ended with 3 frames", before)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "dev-1")
		h.handleAgentWS(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
	}))
	defer srv.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/?session="+session.ID, dev1Headers("dev-1", "dev-1-secret"))
	if err == nil {
		// Let the attach run, then drop it the way a worker told to stop would,
		// so its close reaches the row.
		time.Sleep(100 * time.Millisecond)
		_ = conn.Close()
		time.Sleep(200 * time.Millisecond)
		h.relay.CloseRelay(session.ID)
		t.Error("an agent attached to a session that had already been stopped")
	} else if resp == nil || resp.StatusCode != http.StatusGone {
		t.Errorf("status = %v, want 410 for a session that is not active", resp)
	}

	if h.relay.GetRelay(session.ID) != nil {
		t.Error("a relay exists for a stopped session")
	}
	after := readRow(t, database, session.ID)
	if after.Frames != before.Frames || after.Status != "ended" {
		t.Errorf("row = %+v, want it unchanged from %+v", after, before)
	}
}

// TestEndSessionLeavesAnEndedRowAlone pins the repository half: whatever close
// arrives second must not rewrite the counters or the end time.
func TestEndSessionLeavesAnEndedRowAlone(t *testing.T) {
	h, database, _ := rcFixture(t)
	ctx := context.Background()

	session := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
	if err := h.repo.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.EndSession(ctx, session.ID, 120, 4096, 7); err != nil {
		t.Fatal(err)
	}
	first := readRow(t, database, session.ID)

	time.Sleep(20 * time.Millisecond)
	if err := h.repo.EndSession(ctx, session.ID, 1, 10, 0); err == nil {
		t.Error("EndSession reported success on a row that was already ended")
	}
	second := readRow(t, database, session.ID)
	if second.Frames != 120 || second.Bytes != 4096 || second.Inputs != 7 {
		t.Errorf("counters overwritten: %+v, want 120/4096/7", second)
	}
	if first.EndedAt == nil || second.EndedAt == nil || !first.EndedAt.Equal(*second.EndedAt) {
		t.Errorf("ended_at moved from %v to %v", first.EndedAt, second.EndedAt)
	}
}

// TestARecreatedRelayKeepsTheSessionsMode: a relay rebuilt on attach was always
// full_control, so a view_only session whose relay had to be recreated accepted
// the operator's input.
func TestARecreatedRelayKeepsTheSessionsMode(t *testing.T) {
	h, _, _ := rcFixture(t)

	for _, attach := range []struct {
		name string
		fn   func(string, string, *websocket.Conn) (*activeRelay, bool)
	}{
		{"agent", h.relay.AttachAgent},
		{"operator", h.relay.AttachOperator},
	} {
		t.Run(attach.name, func(t *testing.T) {
			id := NewID()
			relay, ok := attach.fn(id, "view_only", serverSocket(t))
			if !ok {
				t.Fatal("attach refused")
			}
			t.Cleanup(func() { h.relay.CloseRelay(id) })
			if got := relay.CurrentMode(); got != "view_only" {
				t.Errorf("recreated relay mode = %q, want view_only from the session row", got)
			}
		})
	}
}

// TestARelayNothingAttachesToExpires: rc.start is fire-and-forget. An agent that
// never dials, with an operator who never opens the console, used to leave the
// relay registered and the row 'active' until the server restarted.
func TestARelayNothingAttachesToExpires(t *testing.T) {
	h, database, _ := rcFixture(t)
	h.relay.attachTimeout = 50 * time.Millisecond

	idle := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
	live := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
	for _, s := range []*RemoteControlSession{idle, live} {
		if err := h.repo.CreateSession(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		h.relay.RegisterSession(s.ID, s.SessionMode)
	}
	if _, ok := h.relay.AttachAgent(live.ID, live.SessionMode, serverSocket(t)); !ok {
		t.Fatal("attach refused")
	}
	t.Cleanup(func() { h.relay.CloseRelay(live.ID) })

	deadline := time.Now().Add(3 * time.Second)
	for h.relay.GetRelay(idle.ID) != nil || readRow(t, database, idle.ID).Status == "active" {
		if time.Now().After(deadline) {
			t.Fatalf("relay nothing attached to is still registered (row %+v)", readRow(t, database, idle.ID))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if readRow(t, database, idle.ID).EndedAt == nil {
		t.Error("expired session has no ended_at")
	}

	// The expiry is for relays nothing joined; one with an agent on it stays.
	time.Sleep(150 * time.Millisecond)
	if h.relay.GetRelay(live.ID) == nil {
		t.Error("a relay with an attached agent was expired")
	}
	if readRow(t, database, live.ID).Status != "active" {
		t.Error("an attached session's row was ended by the expiry")
	}
}

// TestStartupSweepEndsActiveRows: relays live only in memory, so a row still
// 'active' when the server starts belongs to a process that is gone.
func TestStartupSweepEndsActiveRows(t *testing.T) {
	h, database, _ := rcFixture(t)
	ctx := context.Background()

	var ids []string
	for i := 0; i < 3; i++ {
		s := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
		if err := h.repo.CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
	}
	if err := h.repo.EndSession(ctx, ids[2], 9, 90, 1); err != nil {
		t.Fatal(err)
	}
	finished := readRow(t, database, ids[2])

	n, err := h.repo.EndStaleSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("swept %d rows, want 2", n)
	}
	for _, id := range ids[:2] {
		row := readRow(t, database, id)
		if row.Status != "ended" || row.EndedAt == nil {
			t.Errorf("row %s = %+v after the sweep, want ended with an end time", id, row)
		}
	}
	after := readRow(t, database, ids[2])
	if after.Frames != 9 || !after.EndedAt.Equal(*finished.EndedAt) {
		t.Errorf("sweep touched an already-ended row: %+v, was %+v", after, finished)
	}
}

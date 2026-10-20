package remoteexec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// recordingHub is an online device that accepts every command and keeps them.
type recordingHub struct {
	mu   sync.Mutex
	sent []map[string]any
}

func (h *recordingHub) Online(string) bool { return true }

func (h *recordingHub) SendTo(_ string, b []byte) bool {
	var env map[string]any
	_ = json.Unmarshal(b, &env)
	h.mu.Lock()
	h.sent = append(h.sent, env)
	h.mu.Unlock()
	return true
}

// command waits for the first command of the given name and returns its payload.
func (h *recordingHub) command(t *testing.T, name string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		for _, env := range h.sent {
			if env["command"] == name {
				p, _ := env["payload"].(map[string]any)
				h.mu.Unlock()
				return p
			}
		}
		h.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s command reached the agent", name)
	return nil
}

func (h *recordingHub) count(name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, env := range h.sent {
		if env["command"] == name {
			n++
		}
	}
	return n
}

// dialTerminalQuery opens the operator socket with extra query parameters and
// returns the connection so the test can type into it.
func dialTerminalQuery(t *testing.T, h *Handler, ticket, query string) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "dev-1")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		h.handleTerminalWS(w, r)
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"?ticket="+ticket+"&shell=bash"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// The browser's size travels with term.open, so a pty shell starts at the
// operator's window size rather than 80x24.
func TestTerminalOpenCarriesTheBrowserSize(t *testing.T) {
	h, _, _, ticket := terminalFixture(t, true)
	hub := &recordingHub{}
	h.hub = hub

	dialTerminalQuery(t, h, ticket, "&cols=132&rows=43")
	p := hub.command(t, "term.open")
	if p["cols"] != float64(132) || p["rows"] != float64(43) {
		t.Fatalf("term.open payload = %v, want cols 132 and rows 43", p)
	}
}

// A size that is missing or absurd is left out, so the agent uses its default
// rather than a 0x0 or 65535-wide terminal.
func TestTerminalOpenDropsUnusableSizes(t *testing.T) {
	for _, q := range []string{"", "&cols=0&rows=40", "&cols=99999&rows=40", "&cols=abc&rows=40"} {
		h, _, _, ticket := terminalFixture(t, true)
		hub := &recordingHub{}
		h.hub = hub
		dialTerminalQuery(t, h, ticket, q)
		p := hub.command(t, "term.open")
		if _, ok := p["cols"]; ok {
			t.Errorf("query %q: term.open carried cols %v", q, p["cols"])
		}
	}
}

// term.resize from the browser becomes a term.resize command for the agent,
// bounded the same way.
func TestTerminalResizeIsForwardedAndBounded(t *testing.T) {
	h, _, _, ticket := terminalFixture(t, true)
	hub := &recordingHub{}
	h.hub = hub
	conn := dialTerminalQuery(t, h, ticket, "")
	hub.command(t, "term.open")

	_ = conn.WriteJSON(map[string]any{"type": "term.resize", "cols": 5000, "rows": 40})
	_ = conn.WriteJSON(map[string]any{"type": "term.resize", "cols": 120, "rows": 30})
	p := hub.command(t, "term.resize")
	if p["cols"] != float64(120) || p["rows"] != float64(30) {
		t.Fatalf("term.resize payload = %v, want 120x30", p)
	}
	if n := hub.count("term.resize"); n != 1 {
		t.Fatalf("%d term.resize commands sent, want 1: the 5000-column one must be dropped", n)
	}
}

func TestTerminalModeIsRelayedOnlyForPTYAndOnlyFromTheOwningDevice(t *testing.T) {
	r := NewTerminalRelay()
	frames := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		up := websocket.Upgrader{}
		ws, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		r.Register("s-1", "dev-1", "u-1", ws)
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		for {
			_, b, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var f struct{ Type, Data string }
			_ = json.Unmarshal(b, &f)
			frames <- f.Type + ":" + f.Data
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for r.Get("s-1") == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	ctx := context.Background()
	if err := r.AcceptTerminalMode(ctx, "dev-rogue", "s-1", "pty"); err == nil {
		t.Fatal("a mode from another device was accepted")
	}
	_ = r.AcceptTerminalMode(ctx, "dev-1", "s-1", "<script>")
	_ = r.AcceptTerminalMode(ctx, "dev-1", "s-1", "pty")

	select {
	case f := <-frames:
		if f != "term.mode:pty" {
			t.Fatalf("first frame = %q, want term.mode:pty and nothing before it", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no term.mode frame reached the browser")
	}
}

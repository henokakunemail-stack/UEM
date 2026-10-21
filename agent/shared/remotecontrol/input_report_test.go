package remotecontrol

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// relayPair connects a session to a fake relay: the session holds the client
// end, the test reads and writes the server end the way the relay would.
func relayPair(t *testing.T, s *Session) *websocket.Conn {
	t.Helper()
	got := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		got <- c
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	s.mu.Lock()
	s.conn = client
	s.mu.Unlock()
	relay := <-got
	t.Cleanup(func() { _ = relay.Close() })
	return relay
}

// readControls collects the text control messages the session sends within d.
func readControls(t *testing.T, relay *websocket.Conn, d time.Duration) []map[string]any {
	t.Helper()
	var out []map[string]any
	deadline := time.Now().Add(d)
	for {
		_ = relay.SetReadDeadline(deadline)
		kind, b, err := relay.ReadMessage()
		if err != nil {
			return out
		}
		if kind != websocket.TextMessage {
			out = append(out, map[string]any{"type": "binary-frame"})
			continue
		}
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			out = append(out, m)
		}
	}
}

type refusingCapturer struct {
	fakeCapturer
	mu   sync.Mutex
	keys []InputEvent
}

func (r *refusingCapturer) InjectMouseEvent(InputEvent) error {
	return errors.New("SendInput mouse failed: Access is denied.")
}

func (r *refusingCapturer) InjectKeyboardEvent(e InputEvent) error {
	r.mu.Lock()
	r.keys = append(r.keys, e)
	r.mu.Unlock()
	return nil
}

// Every injection error used to be discarded: the operator saw a live desktop
// that did not respond, and nothing said why. A refusal is now reported, once
// per throttle window rather than on every mouse move.
func TestRefusedInputIsReportedOnce(t *testing.T) {
	c := &refusingCapturer{fakeCapturer: fakeCapturer{capture: true}}
	s := NewSession(SessionConfig{SessionID: "s-1", Mode: "full_control"}, "http://localhost:1", Credentials{}, c)
	relay := relayPair(t, s)
	go s.readInputLoop()

	for i := 0; i < 5; i++ {
		if err := relay.WriteJSON(map[string]any{"type": "mouse", "action": "move", "x": i, "y": i}); err != nil {
			t.Fatal(err)
		}
	}
	var failed []map[string]any
	for _, m := range readControls(t, relay, time.Second) {
		if m["type"] == "input_failed" {
			failed = append(failed, m)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("input_failed notices = %d, want exactly 1 for a burst of refused moves", len(failed))
	}
	if reason, _ := failed[0]["reason"].(string); !strings.Contains(reason, "Access is denied") {
		t.Fatalf("notice reason = %q, want the injection error", reason)
	}
}

// keyCode cannot tell right Ctrl from left; the browser's location can. A
// right Ctrl (AltGr's half on many layouts) used to arrive as left Ctrl.
func TestKeyLocationPicksTheRightModifier(t *testing.T) {
	c := &refusingCapturer{fakeCapturer: fakeCapturer{capture: true}}
	s := NewSession(SessionConfig{SessionID: "s-1", Mode: "full_control"}, "http://localhost:1", Credentials{}, c)
	relay := relayPair(t, s)
	go s.readInputLoop()

	for _, m := range []map[string]any{
		{"type": "keyboard", "action": "down", "code": 17, "location": 2},
		{"type": "keyboard", "action": "down", "code": 16, "location": 1},
		{"type": "keyboard", "action": "down", "code": 65},
	} {
		if err := relay.WriteJSON(m); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		n := len(c.keys)
		c.mu.Unlock()
		if n == 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.keys) != 3 || c.keys[0].Code != 0xA3 || c.keys[1].Code != 0xA0 || c.keys[2].Code != 65 {
		t.Fatalf("injected codes = %+v, want 0xA3, 0xA0, 65", c.keys)
	}
}

// A service with no logged-on user to hand the session to used to stream its
// own invisible Session 0 desktop. It now says why there is nothing to control.
func TestAnUnavailableDesktopIsExplained(t *testing.T) {
	s := NewSession(SessionConfig{SessionID: "s-1", Mode: "full_control"}, "http://localhost:1", Credentials{},
		NewUnavailableCapturer("no desktop to control: nobody is logged on"))
	relay := relayPair(t, s)
	go s.streamFramesLoop()

	for _, m := range readControls(t, relay, time.Second) {
		if m["type"] == "unsupported" {
			if reason, _ := m["reason"].(string); strings.Contains(reason, "nobody is logged on") {
				return
			}
		}
	}
	t.Fatal("no unsupported notice carrying the reason")
}

// scriptedCapturer returns the same frame, except for a run of failures.
type scriptedCapturer struct {
	fakeCapturer
	mu    sync.Mutex
	calls int
	fail  func(call int) bool
}

func (c *scriptedCapturer) CaptureScreen() ([]byte, int, int, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	if c.fail(n) {
		return nil, 0, 0, errors.New("bitblt failed: Access is denied.")
	}
	return c.fakeCapturer.CaptureScreen()
}

// After a UAC prompt or the lock screen goes away, the desktop often looks
// exactly as it did before. The damage diff then found nothing to send, and
// the console kept its "no signal" overlay over a desktop that was back. A
// full frame now follows a reported failure.
func TestRecoveryFromAReportedFailureSendsAFrame(t *testing.T) {
	c := &scriptedCapturer{fakeCapturer: fakeCapturer{capture: true}, fail: func(n int) bool { return n >= 2 && n <= 4 }}
	s := NewSession(SessionConfig{SessionID: "s-1", Mode: "full_control"}, "http://localhost:1", Credentials{}, c)
	relay := relayPair(t, s)
	go s.streamFramesLoop()

	frames, sawFailure, frameAfterFailure := 0, false, false
	for _, m := range readControls(t, relay, 2*time.Second) {
		switch m["type"] {
		case "binary-frame":
			frames++
			if sawFailure {
				frameAfterFailure = true
			}
		case "capture_failed":
			sawFailure = true
		}
	}
	if frames == 0 || !sawFailure {
		t.Fatalf("frames=%d failure=%v; the fixture did not exercise a reported failure", frames, sawFailure)
	}
	if !frameAfterFailure {
		t.Fatal("no frame was sent after capture recovered on an unchanged desktop")
	}
}

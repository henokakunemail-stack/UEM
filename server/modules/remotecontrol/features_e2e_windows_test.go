//go:build windows

package remotecontrol

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	agentrc "github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/remotecontrol"
	"net/http/httptest"
)

// TestRemoteControlFeaturesEndToEnd drives the real agent session -- real screen
// capture, real clipboard -- through the real relay handlers, with
// a scripted console on the operator socket. It is the path an operator's click
// takes, minus the browser: hello, frames, monitor switching, clipboard and a
// file upload, each answered by the endpoint.
func TestRemoteControlFeaturesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("drives the real desktop")
	}
	capturer := agentrc.NewPlatformCapturer()
	if !capturer.Capabilities().Capture {
		t.Skip("this host cannot be captured")
	}

	h, _, _ := rcRouteFixture(t)
	h.hub = &acceptingHub{}
	session := &RemoteControlSession{DeviceID: "dev-1", OperatorID: "u-1", SessionMode: "full_control"}
	if err := h.repo.CreateSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	h.relay.RegisterSession(session.ID, session.SessionMode)
	ticket := issueTicket(t, h, "u-1", "tech")

	withDevice := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", "dev-1")
			fn(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/agent", withDevice(h.handleAgentWS))
	mux.HandleFunc("/operator", withDevice(h.handleOperatorWS))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	wsBase := "ws" + strings.TrimPrefix(srv.URL, "http")

	// Operator first, so nothing the agent sends is buffered or lost.
	op, _, err := websocket.DefaultDialer.Dial(wsBase+"/operator?session="+session.ID+"&ticket="+ticket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()

	agent := agentrc.NewSession(agentrc.SessionConfig{
		SessionID: session.ID, Mode: "full_control", RelayURL: "/agent?session=" + session.ID, Operator: "e2e-test",
	}, srv.URL, agentrc.Credentials{DeviceID: "dev-1", DeviceSecret: "dev-1-secret"}, capturer)
	if err := agent.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer agent.Stop()

	// next reads until a control message of the wanted type arrives, counting
	// the binary frames that went past on the way.
	frames := 0
	next := func(want string) map[string]any {
		t.Helper()
		_ = op.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			kind, data, err := op.ReadMessage()
			if err != nil {
				t.Fatalf("waiting for %q: %v", want, err)
			}
			if kind == websocket.BinaryMessage {
				frames++
				continue
			}
			var m map[string]any
			if json.Unmarshal(data, &m) == nil && m["type"] == want {
				return m
			}
		}
	}
	send := func(v map[string]any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if err := op.WriteMessage(websocket.TextMessage, b); err != nil {
			t.Fatal(err)
		}
	}

	hello := next("hello")
	caps := hello["capabilities"].(map[string]any)
	if caps["clipboard"] != true || caps["file_transfer"] != true {
		t.Fatalf("hello does not advertise the features: %v", caps)
	}
	monitors, _ := caps["monitors"].([]any)
	if len(monitors) == 0 {
		t.Fatalf("hello lists no monitors: %v", caps)
	}

	// Frames are flowing: the stats message is sent from the capture loop.
	send(map[string]any{"type": "refresh"})
	// The first stats message goes out on the first tick, ahead of that
	// tick's frame; by the second, a second of frames has been written.
	next("stats")
	stats := next("stats")
	if q, _ := stats["quality"].(float64); q < 35 || q > 80 {
		t.Fatalf("stats quality %v outside the adaptive range", stats["quality"])
	}
	if frames == 0 {
		t.Fatal("no screen frame arrived before the stats message")
	}

	// All monitors, then back to the first: each answered with its size.
	send(map[string]any{"type": "monitor", "index": -1})
	if m := next("monitor_changed"); m["width"].(float64) <= 0 {
		t.Fatalf("monitor_changed for all monitors: %v", m)
	}
	send(map[string]any{"type": "monitor", "index": 0})
	next("monitor_changed")

	// Clipboard: the operator's text lands on the endpoint.
	cb := capturer.(interface {
		ClipboardText() (string, error)
		SetClipboardText(string) (uint32, error)
	})
	saved, _ := cb.ClipboardText()
	defer func() { _, _ = cb.SetClipboardText(saved) }()
	want := "e2e clipboard " + time.Now().Format(time.RFC3339Nano)
	send(map[string]any{"type": "clipboard", "text": want})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got, _ := cb.ClipboardText(); got == want {
			break
		}
		if time.Now().After(deadline) {
			got, _ := cb.ClipboardText()
			t.Fatalf("endpoint clipboard holds %q, want %q", got, want)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// File upload: two chunks, saved under the user's Remote Transfers folder.
	body := []byte(strings.Repeat("remote-control e2e upload\n", 4000))
	half := len(body) / 2
	send(map[string]any{"type": "file_begin", "id": "e2e", "name": "rc-e2e-upload.txt", "size": len(body)})
	send(map[string]any{"type": "file_chunk", "id": "e2e", "seq": 0, "data": base64.StdEncoding.EncodeToString(body[:half])})
	send(map[string]any{"type": "file_chunk", "id": "e2e", "seq": 1, "data": base64.StdEncoding.EncodeToString(body[half:])})
	send(map[string]any{"type": "file_end", "id": "e2e"})
	saved_ := next("file_saved")
	path, _ := saved_["path"].(string)
	defer os.Remove(path)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(body) {
		t.Fatalf("uploaded file at %q: %d bytes, err=%v", path, len(got), err)
	}
	if !strings.Contains(path, "Remote Transfers") {
		t.Fatalf("upload landed outside Remote Transfers: %s", path)
	}

	// View-only: a clipboard write is dropped by the relay before it reaches
	// the endpoint.
	send(map[string]any{"type": "mode", "mode": "view_only"})
	time.Sleep(200 * time.Millisecond)
	send(map[string]any{"type": "clipboard", "text": "must not arrive"})
	time.Sleep(500 * time.Millisecond)
	if got, _ := cb.ClipboardText(); got == "must not arrive" {
		t.Fatal("a view_only console changed the endpoint clipboard")
	}
	t.Logf("frames=%d monitors=%d upload=%s", frames, len(monitors), path)
}

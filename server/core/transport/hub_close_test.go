package transport

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialPair returns both ends of one WebSocket: the server side the hub holds,
// and the client side the agent would read from.
func dialPair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	got := make(chan *websocket.Conn, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		got <- conn
		select {}
	}))
	t.Cleanup(up.Close)
	cl, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(up.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return <-got, cl
}

// At shutdown every agent is told the server is going away, so it reconnects
// at once -- to another node, behind a load balancer -- instead of waiting out
// its read deadline against a process that is exiting. Nothing new registers
// after that.
func TestCloseSendsGoingAwayAndRefusesNewAgents(t *testing.T) {
	hub := NewHub()
	server, client := dialPair(t)
	hub.Register("dev-1", server)

	hub.Close()

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := client.ReadMessage()
	var ce *websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.CloseGoingAway {
		t.Fatalf("agent read %v after Close, want a going-away close frame", err)
	}
	if hub.Count() != 0 || hub.Online("dev-1") {
		t.Fatal("a device is still registered after Close")
	}

	late, _ := dialPair(t)
	hub.Register("dev-2", late)
	if hub.Online("dev-2") {
		t.Fatal("an agent registered after Close")
	}
}

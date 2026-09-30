package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialAgent returns a client socket and the server side of it, for tests that
// need a real *websocket.Conn because the Hub stores one.
func dialAgent(t *testing.T) (client *websocket.Conn, server *websocket.Conn) {
	t.Helper()
	got := make(chan *websocket.Conn, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		got <- conn
		select {} // hold the socket open until the test tears it down
	}))
	t.Cleanup(up.Close)

	cl, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(up.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl, <-got
}

// TestSendOnAConnBeingTornDownNeverPanics is the regression test for the only
// path to total process death in the server.
//
// Conn.Send wrote to c.send, and closeSend closed that same channel. Two
// goroutines reach both: Hub.SendTo's callers, and the read loop's own deferred
// cleanup. Nothing ordered them, so a send landing on a closed channel panicked
// -- and unlike the nine HTTP handlers, taskscheduler/scheduler.go:86 sits on a
// bare go func() behind a chi router with no middleware.Recoverer, so there the
// panic took the process down and with it every connected agent.
//
// The check that the queue is full was never a guard against this. Go picks
// uniformly at random among the ready cases in a select, and a send on a closed
// channel panics whenever that case is picked, so the default arm only ever
// made the panic probabilistic. This test asserts the stronger property: the
// send channel is never closed, so there is nothing to panic on.
func TestSendOnAConnBeingTornDownNeverPanics(t *testing.T) {
	_, server := dialAgent(t)
	hub := NewHub()
	c := hub.Register("dev-1", server)

	// Tear the connection down the way the read loop's defer does, repeatedly,
	// while senders are hammering it.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var panics int
	var pm sync.Mutex

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							pm.Lock()
							panics++
							pm.Unlock()
						}
					}()
					c.Send([]byte("ping"))
				}()
			}
		}()
	}

	// The teardown races the senders, which is the point: the panic needed
	// exactly this interleaving, and it needs it to be reachable, not likely.
	for i := 0; i < 200; i++ {
		c.closeSend()
		_ = c.ws.Close()
		c = newConn("dev-1", server)
	}

	close(stop)
	wg.Wait()

	pm.Lock()
	got := panics
	pm.Unlock()
	if got != 0 {
		t.Errorf("%d sends panicked while the connection was being torn down; the "+
			"first one on a bare goroutine takes the whole server down", got)
	}
}

// TestSendIsNotClosed pins the mechanism rather than the symptom. The fix above
// removed the close entirely, because a select cannot guard it -- the arm that
// observes the close is always ready alongside the send arm, so Go picks
// between them at random and a 50% panic rate is what that actually produces.
//
// If a future change reintroduces close(send), this fails immediately and
// cheaply, instead of leaving a one-in-two crash for whoever hits a reconnect
// while a command is in flight.
func TestSendIsNotClosed(t *testing.T) {
	_, server := dialAgent(t)
	hub := NewHub()
	c := hub.Register("dev-1", server)

	c.closeSend()
	c.closeSend() // twice: closeOnce must absorb the repeat

	// Drain a fresh copy of the channel: a closed channel reads back instantly
	// and forever, so a receive that has to block proves it is still open.
	select {
	case <-c.send:
		t.Fatal("send was closed by closeSend; a send can panic on it")
	default:
	}

	c.Send([]byte("still open"))
}

// TestWritePumpStopsWhenTheConnectionIsTornDown: the reason done exists. The
// pump has to end on closeSend, and it has to do so without waiting for the
// queue to drain, because a wedged peer means the queue is exactly what is not
// happening.
func TestWritePumpStopsWhenTheConnectionIsTornDown(t *testing.T) {
	_, server := dialAgent(t)
	hub := NewHub()
	c := hub.Register("dev-1", server)

	finished := make(chan struct{})
	go c.writePump(finished)

	c.closeSend()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("writePump did not return after closeSend; the goroutine outlives " +
			"its connection and keeps a dead socket open")
	}
}

// TestWritePumpStillDeliversWhatWasQueuedFirst: the new select must not drop
// messages that were already accepted before shutdown.
func TestWritePumpStillDeliversWhatWasQueuedFirst(t *testing.T) {
	_, server := dialAgent(t)
	hub := NewHub()
	c := hub.Register("dev-1", server)

	if !c.Send([]byte("first")) {
		t.Fatal("Send refused a message into an empty queue")
	}
	finished := make(chan struct{})
	go c.writePump(finished)
	c.closeSend()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("writePump hung")
	}
}

// TestSupersededConnectionStopsWithoutRacingItsPump covers the second half of
// the finding. Register used to spawn `go old.closeAndLog(deviceID)`, which
// closed old.send from outside the connection's own read loop while its
// writePump was still selecting on that channel. The comment above it claimed
// closing old.send from Register "would race writePump" and then did it three
// lines later.
//
// The replacement closes only the socket, and the test reproduces the whole
// production lifecycle rather than a piece of it: ws.go:158-172 runs a read
// loop whose deferred cleanup calls closeSend, waits for the pump, and only
// then closes the socket. A test that started a writePump on its own would hang
// forever and prove nothing, because nothing in it would ever call closeSend.
func TestSupersededConnectionStopsWithoutRacingItsPump(t *testing.T) {
	_, firstServer := dialAgent(t)
	_, secondServer := dialAgent(t)
	hub := NewHub()

	first := hub.Register("dev-1", firstServer)
	finished := make(chan struct{})
	go first.writePump(finished)

	// The read loop and its deferred cleanup, as ws.go runs them.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := firstServer.ReadMessage(); err != nil {
				first.closeSend()
				<-finished
				_ = firstServer.Close()
				return
			}
		}
	}()

	// A reconnect supersedes it while the read loop and pump are both live.
	hub.Register("dev-1", secondServer)

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the superseded connection's writePump never stopped; a leak here " +
			"is one goroutine and one socket per duplicate connect")
	}
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the superseded connection's read loop never unwound: Register " +
			"closed its send channel from a goroutine of its own, so the loop had " +
			"no error to return on")
	}

	// The hub resolves to the new connection, and the old one is untouched --
	// which is what makes the next reconnect able to supersede it again.
	if hub.Get("dev-1") == first {
		t.Error("the hub still resolves the superseded connection")
	}
}

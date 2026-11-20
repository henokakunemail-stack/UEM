package httpguard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// slowBody serves size bytes in chunks with a pause between them, so the whole
// response takes longer than the server's WriteTimeout while no single gap
// does.
func slowBody(w http.ResponseWriter, chunks int, pause time.Duration) {
	buf := make([]byte, 1024)
	for i := 0; i < chunks; i++ {
		if _, err := w.Write(buf); err != nil {
			return
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(pause)
	}
}

func newSlowServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.Config.WriteTimeout = 150 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func fetchLen(t *testing.T, url string) (int, error) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return len(b), err
}

// The regression: a response that keeps moving but outlasts WriteTimeout was
// cut off mid-body ("unexpected EOF" on the agent side).
func TestLongTransferOutlivesWriteTimeoutWhileMoving(t *testing.T) {
	const chunks = 10 // 10 x 40ms = 400ms, well past the 150ms WriteTimeout
	srv := newSlowServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowBody(LongTransfer(w, 100*time.Millisecond), chunks, 40*time.Millisecond)
	}))
	n, err := fetchLen(t, srv.URL)
	if err != nil {
		t.Fatalf("download cut off after %d bytes: %v", n, err)
	}
	if n != chunks*1024 {
		t.Fatalf("got %d bytes, want %d", n, chunks*1024)
	}
}

// The control: without LongTransfer the same handler is truncated, which is
// what proves the test above exercises the timeout at all.
func TestWithoutLongTransferWriteTimeoutTruncates(t *testing.T) {
	srv := newSlowServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowBody(w, 10, 40*time.Millisecond)
	}))
	n, err := fetchLen(t, srv.URL)
	if err == nil && n == 10*1024 {
		t.Fatal("expected the server-wide WriteTimeout to cut the body short")
	}
}

// A client that has stopped receiving must still be dropped: once the socket
// buffers fill, Write blocks, the deadline is not pushed forward, and the
// write fails after idle.
func TestLongTransferStillClosesStalledTransfer(t *testing.T) {
	done := make(chan error, 1)
	srv := newSlowServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lw := LongTransfer(w, 200*time.Millisecond)
		buf := make([]byte, 64*1024)
		for i := 0; i < 4096; i++ { // 256 MB, far beyond any socket buffer
			if _, err := lw.Write(buf); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}))
	resp, err := http.Get(srv.URL) // read the headers, then never the body
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("handler wrote 256 MB to a client that never read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stalled transfer was never closed")
	}
}

// The router wraps every writer in chi's WrapResponseWriter (metrics
// middleware); the deadline must still reach the connection through it.
func TestLongTransferWorksThroughChiWrapper(t *testing.T) {
	srv := newSlowServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		slowBody(LongTransfer(ww, 100*time.Millisecond), 10, 40*time.Millisecond)
	}))
	n, err := fetchLen(t, srv.URL)
	if err != nil || n != 10*1024 {
		t.Fatalf("got %d bytes, err %v", n, err)
	}
}

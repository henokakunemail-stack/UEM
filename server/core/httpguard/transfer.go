package httpguard

import (
	"net/http"
	"time"
)

// TransferIdleTimeout is how long a large download may go without the client
// taking another chunk before the server gives up on it.
const TransferIdleTimeout = 2 * time.Minute

// LongTransfer lifts the server-wide WriteTimeout for one response that sends
// a file, replacing it with a deadline that moves forward every time a chunk
// is written.
//
// The server's WriteTimeout (60s) is a deadline on the whole response, not on
// silence. Agent binaries are 15-30 MB and software packages are far larger,
// so a branch office on a slow link was cut off at the 60-second mark midway
// through the body: the agent saw "unexpected EOF" and the update failed every
// time, on every retry, at roughly the same byte count. Simply removing the
// deadline would let a client that stops reading hold the connection open
// forever, so instead each write pushes the deadline idle into the future: a
// transfer that keeps moving can run as long as it needs, and one that stalls
// is still closed.
//
// If the underlying writer cannot set deadlines the original writer is
// returned unchanged and the server-wide timeout still applies.
func LongTransfer(w http.ResponseWriter, idle time.Duration) http.ResponseWriter {
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Now().Add(idle)); err != nil {
		return w
	}
	return &progressDeadlineWriter{ResponseWriter: w, rc: rc, idle: idle}
}

type progressDeadlineWriter struct {
	http.ResponseWriter
	rc   *http.ResponseController
	idle time.Duration
}

func (p *progressDeadlineWriter) Write(b []byte) (int, error) {
	_ = p.rc.SetWriteDeadline(time.Now().Add(p.idle))
	return p.ResponseWriter.Write(b)
}

func (p *progressDeadlineWriter) Flush() { _ = p.rc.Flush() }

// Unwrap lets http.ResponseController reach the connection beneath.
func (p *progressDeadlineWriter) Unwrap() http.ResponseWriter { return p.ResponseWriter }

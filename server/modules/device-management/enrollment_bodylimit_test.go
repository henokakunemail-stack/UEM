package devicemanagement

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// countingReader records how many bytes the server actually pulled off the
// wire.
//
// This is the measurement that matters, and it is exact: the vulnerability was
// never "a big body was accepted" but "the whole body was read before anything
// decided to reject it". MemStats deltas would prove it too, but they are
// noisy enough to flake on a shared host; a byte count is not.
type countingReader struct {
	src *bytes.Reader
	n   int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.n += n
	return n, err
}

func newEnrollTestHandler(t *testing.T) (*chi.Mux, *sqlx.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "enroll.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	r := chi.NewRouter()
	NewEnrollmentHandler(NewRepository(d), d).Register(r)
	return r, d
}

// TestEnrollRejectsOversizedBodyWithoutDecoding is the end-to-end proof on the
// one route that is both unauthenticated and unbounded.
//
// No credentials are supplied, and none are needed: the point is that the
// server stops reading before it ever gets to find that out.
//
// The body is valid JSON carrying one huge string. That is deliberate and it is
// the whole shape of the attack. A body of "aaaa..." is not a smaller test, it
// is a different test: encoding/json fails on the very first byte and reads
// nothing more, so it never reaches the cap at all -- an earlier draft of this
// test did exactly that and passed for the wrong reason, reporting 400 on a
// route that was in fact fully protected.
func TestEnrollRejectsOversizedBodyWithoutDecoding(t *testing.T) {
	r, _ := newEnrollTestHandler(t)

	const bodySize = 8 << 20
	cr := &countingReader{src: bytes.NewReader(oversizedJSONBody(bodySize))}

	req := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", cr)
	rec := httptest.NewRecorder()

	start := time.Now()
	r.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d\nbody: %s",
			rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}

	// The cap is 1 MiB; MaxBytesReader needs to read one byte past it to notice,
	// and copies through a buffer, so allow a little slack and assert only that
	// the body was not taken in full.
	const slack = 64 << 10
	if cr.n > (1<<20)+slack {
		t.Errorf("server consumed %d bytes of an %d byte body; the cap did not hold",
			cr.n, bodySize)
	}
	if cr.n >= bodySize {
		t.Errorf("server consumed the whole %d byte body before rejecting it", bodySize)
	}

	// Not the primary assertion, but a body this size read end-to-end is slow
	// enough that a regression here shows up as a wall-clock jump.
	if elapsed > 2*time.Second {
		t.Errorf("took %v to reject an %d byte body", elapsed, bodySize)
	}

	// The answer has to name the size. A 413 that says "invalid json" reads as
	// a malformed request and invites the sender to retry smaller forever.
	if msg := rec.Body.String(); !strings.Contains(msg, "1 MiB") {
		t.Errorf("413 body does not mention the limit: %s", msg)
	}
}

// oversizedJSONBody builds a syntactically valid enrollment request whose
// hostname field is `total` bytes of letter, so the decoder has to walk the
// entire string before it can decide the document is complete.
func oversizedJSONBody(total int) []byte {
	const prefix = `{"enrollment_token":"nope","hostname":"`
	const suffix = `","os_name":"windows"}`
	pad := total - len(prefix) - len(suffix)
	if pad < 0 {
		pad = 0
	}
	return []byte(prefix + strings.Repeat("h", pad) + suffix)
}

// TestEnrollStillAcceptsASmallBody pins the other side of the boundary: the cap
// must not have broken the route it was added to.
func TestEnrollStillAcceptsASmallBody(t *testing.T) {
	r, _ := newEnrollTestHandler(t)

	// No token, so this cannot enroll anything -- but it must get past the cap
	// and be answered on its merits, not on its size.
	body := `{"enrollment_token":"nope","hostname":"pc-1","os_name":"windows"}`
	req := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d (a bad token is not a bad size)\nbody: %s",
			rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

// TestTheCapIsNotRouterWide guards the decision to apply the cap per route
// rather than as router middleware.
//
// The package upload route legitimately accepts hundreds of megabytes, so a
// global cap would be a worse bug than no cap. This asserts the mount stays
// per-route: mounting enrollment's cap over the whole router would have shown up
// here as a large body to some other handler being cut off, and the fix would
// be to re-apply the cap locally rather than to delete this guard.
func TestTheCapIsNotRouterWide(t *testing.T) {
	r, _ := newEnrollTestHandler(t)

	sentinel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	r.Get("/api/some/other/route", sentinel)

	big := strings.Repeat("b", 4<<20)
	req := httptest.NewRequest(http.MethodGet, "/api/some/other/route", strings.NewReader(big))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d: a body cap leaked onto a route it does "+
			"not belong to", rec.Code, http.StatusTeapot)
	}
}

// TestTheBodyIsActuallyConsumedOnASmallRequest is the sanity check that pairs
// with the above: the counting reader sees a real, complete read when the body
// is under the cap. Without it, a "fix" that dropped the body on the floor would
// pass the 413 test by never reading anything.
//
// It is driven through the route too, not through io.LimitReader, so it uses
// the cap the server really applies rather than a copy of the constant.
func TestTheBodyIsActuallyConsumedOnASmallRequest(t *testing.T) {
	r, _ := newEnrollTestHandler(t)

	const body = `{"enrollment_token":"t","hostname":"h"}`
	cr := &countingReader{src: bytes.NewReader([]byte(body))}

	req := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", cr)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if cr.n != len(body) {
		t.Errorf("read %d of %d bytes; the under-cap path is not reading the body",
			cr.n, len(body))
	}
	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Errorf("a %d byte body was refused for size: %s", len(body), rec.Body.String())
	}
}

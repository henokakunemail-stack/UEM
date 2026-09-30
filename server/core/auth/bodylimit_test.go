package auth

// Body-size caps on the two unauthenticated auth routes.
//
// login and refresh decode json.NewDecoder(r.Body) before they check anything,
// so uncapped they let an anonymous caller make the server buffer and parse an
// arbitrary number of bytes. That is the same exposure the enrollment route had;
// these tests exist so removing the cap from either one is caught here rather
// than in production.
//
// The router is driven in-process, not over httptest.Server, so the byte count
// is exact. Over a real connection the count would include headers and chunk
// framing, and the assertion would be about a number that changes when Go
// changes its transport.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// countingReader records how many bytes the server pulled off the body.
type countingReader struct {
	src *bytes.Reader
	n   int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.n += n
	return n, err
}

// capProbe is the 1 MiB limit, restated. httpguard.JSONBodyLimit is not
// imported: this test is deliberately able to fail if the cap is lowered on
// purpose, and a test that reads the constant it is testing cannot.
const capProbe = 1 << 20

func newAuthRouter(t *testing.T) (*chi.Mux, *LoginHandler) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "bodylimit.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	jwtSvc := NewJWTService(testSecret, 15*time.Minute, time.Hour)
	h := NewLoginHandler(d, jwtSvc)
	t.Cleanup(h.Close)

	r := chi.NewRouter()
	h.Register(r)
	return r, h
}

// oversizedLoginBody is valid JSON whose password field is `total` bytes long,
// so the decoder has to walk the whole string before it can decide the document
// is finished.
//
// The valid-JSON part is the point. A body of "aaaa..." is not a cheaper
// version of this test, it is a different one: encoding/json fails on the first
// byte and never reaches the cap, so it would pass against an uncapped server
// and prove nothing about the attack.
func oversizedLoginBody(total int) []byte {
	const prefix = `{"username":"admin","password":"`
	const suffix = `"}`
	pad := total - len(prefix) - len(suffix)
	if pad < 0 {
		pad = 0
	}
	return []byte(prefix + strings.Repeat("p", pad) + suffix)
}

func TestLoginRejectsAnOversizedBodyWithoutReadingIt(t *testing.T) {
	r, _ := newAuthRouter(t)

	const bodySize = 8 << 20
	cr := &countingReader{src: bytes.NewReader(oversizedLoginBody(bodySize))}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", cr)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d\nbody: %s",
			rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if cr.n > capProbe+(64<<10) {
		t.Errorf("server consumed %d bytes of an %d byte body; the cap did not hold",
			cr.n, bodySize)
	}
	if cr.n >= bodySize {
		t.Errorf("server consumed the whole %d byte body before rejecting it", bodySize)
	}
	if msg := rec.Body.String(); !strings.Contains(msg, "1 MiB") {
		t.Errorf("413 body does not mention the limit: %s", msg)
	}
}

func TestRefreshRejectsAnOversizedBodyWithoutReadingIt(t *testing.T) {
	r, _ := newAuthRouter(t)

	const bodySize = 8 << 20
	const prefix = `{"refresh_token":"`
	const suffix = `"}`
	cr := &countingReader{
		src: bytes.NewReader([]byte(prefix + strings.Repeat("t", bodySize-len(prefix)-len(suffix)) + suffix)),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", cr)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d\nbody: %s",
			rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if cr.n >= bodySize {
		t.Errorf("server consumed the whole %d byte body before rejecting it", bodySize)
	}
}

// TestASmallLoginBodyIsReadInFull is the other half of the boundary. A cap that
// broke real logins would be worse than no cap, and the natural way to break it
// by accident is to reject on ContentLength instead of on bytes actually read.
func TestASmallLoginBodyIsReadInFull(t *testing.T) {
	r, _ := newAuthRouter(t)

	const body = `{"username":"admin","password":"short"}`
	cr := &countingReader{src: bytes.NewReader([]byte(body))}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", cr)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if cr.n != len(body) {
		t.Errorf("read %d of %d bytes; the under-cap path is not reading the body",
			cr.n, len(body))
	}
	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Errorf("a %d byte body was refused for size: %s", len(body), rec.Body.String())
	}
	// Unknown user, so 401. That is the answer on its merits rather than on its
	// size, which is what distinguishes "capped" from "broken".
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

// TestTheBodyCapDoesNotReachTheAuthenticatedRoutes: logout, listSessions and
// revokeSession sit behind RequireAuth and read no body, so they carry no cap.
// Mounting one on them would be harmless today and a latent 413 on a future
// route that did read a body, so the cap stays on the two routes that decode an
// anonymous caller's body and nowhere else.
func TestTheBodyCapDoesNotReachTheAuthenticatedRoutes(t *testing.T) {
	r, _ := newAuthRouter(t)

	// No Authorization header: RequireAuth answers first, so this never reaches
	// any body handling. It asserts the routes exist and are guarded, not that
	// the cap is absent -- the cap is a property of the anonymous pair, and the
	// pairing is what TestLoginRejects.. and TestRefreshRejects.. pin.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d: logout is reachable without a token",
			rec.Code, http.StatusUnauthorized)
	}
}

package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
)

// seenAddr runs a request with the given peer and X-Forwarded-For through h
// and returns the RemoteAddr the handler behind it saw.
func seenAddr(h func(http.Handler) http.Handler, peer, xff string) string {
	var got string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.RemoteAddr })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = peer
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	h(inner).ServeHTTP(httptest.NewRecorder(), req)
	return got
}

// A client on the internet cannot choose its own address by sending
// X-Forwarded-For; a trusted proxy can tell us the client's.
func TestClientIPMiddlewareTrustsForwardedOnlyFromProxies(t *testing.T) {
	mw := NewClientIPResolver([]string{"10.0.0.5"}).Middleware

	if got := seenAddr(mw, "203.0.113.9:4444", "1.2.3.4"); got != "203.0.113.9:4444" {
		t.Errorf("untrusted peer spoofing X-Forwarded-For: handler saw %q, want the real peer", got)
	}
	if got := seenAddr(mw, "203.0.113.9:4444", "127.0.0.1"); got != "203.0.113.9:4444" {
		t.Errorf("untrusted peer claiming loopback: handler saw %q, want the real peer", got)
	}
	if got := seenAddr(mw, "10.0.0.5:8080", "198.51.100.7"); got != "198.51.100.7" {
		t.Errorf("trusted proxy forwarding a client: handler saw %q, want 198.51.100.7", got)
	}
	if got := seenAddr(mw, "10.0.0.5:8080", "6.6.6.6, 198.51.100.7"); got != "198.51.100.7" {
		t.Errorf("client-prepended hop behind a trusted proxy: handler saw %q, want the hop the proxy saw", got)
	}
}

// Documents why RealIP was removed: it hands the header straight through.
func TestChiRealIPWouldHaveTrustedASpoofedHeader(t *testing.T) {
	//nolint:staticcheck // the deprecated middleware is exercised on purpose.
	if got := seenAddr(middleware.RealIP, "203.0.113.9:4444", "1.2.3.4"); got != "1.2.3.4" {
		t.Skipf("chi RealIP no longer copies the header (saw %q); nothing to document", got)
	}
}

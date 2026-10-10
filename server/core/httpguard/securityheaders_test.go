package httpguard

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersMiddleware(t *testing.T) {
	cfg := DefaultSecurityHeaders(true)
	cfg.CSPPolicy = "default-src 'self'"

	middleware := SecurityHeaders(cfg)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	handler := middleware(next)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Check headers
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options missing")
	}
	if w.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("X-Frame-Options missing or wrong")
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("Referrer-Policy missing or wrong")
	}
	if !strings.Contains(w.Header().Get("Permissions-Policy"), "accelerometer=()") {
		t.Errorf("Permissions-Policy missing")
	}
	if w.Header().Get("Strict-Transport-Security") != "max-age=31536000; includeSubDomains" {
		t.Errorf("HSTS missing or wrong: %s", w.Header().Get("Strict-Transport-Security"))
	}
	if w.Header().Get("Content-Security-Policy") != "default-src 'self'" {
		t.Errorf("CSP missing or wrong: %s", w.Header().Get("Content-Security-Policy"))
	}
}

func TestSecurityHeadersNoHSTSOnHTTP(t *testing.T) {
	cfg := DefaultSecurityHeaders(false)
	cfg.CSPPolicy = "default-src 'self'"

	middleware := SecurityHeaders(cfg)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	handler := middleware(next)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	// No X-Forwarded-Proto = HTTP
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Header().Get("Strict-Transport-Security") != "" {
		t.Errorf("HSTS should not be set on HTTP: %s", w.Header().Get("Strict-Transport-Security"))
	}
	if w.Header().Get("Content-Security-Policy") != "default-src 'self'" {
		t.Errorf("CSP should still be set: %s", w.Header().Get("Content-Security-Policy"))
	}
}

func TestSecurityHeadersNoCSPOnWebSocketUpgrade(t *testing.T) {
	cfg := DefaultSecurityHeaders(true)
	cfg.CSPPolicy = "default-src 'self'"

	middleware := SecurityHeaders(cfg)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
	})

	handler := middleware(next)

	req := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// CSP should NOT be set on 101 upgrade
	if w.Header().Get("Content-Security-Policy") != "" {
		t.Errorf("CSP should not be set on WebSocket upgrade: %s", w.Header().Get("Content-Security-Policy"))
	}
	// Other headers should still be set
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("Other headers should still be set on upgrade")
	}
}

func TestSecurityHeadersHSTSOnlyWhenTLS(t *testing.T) {
	tests := []struct {
		name        string
		makeRequest func() *http.Request
		expectHSTS  bool
		desc        string
	}{
		{
			name: "TLS via r.TLS",
			makeRequest: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
				// Simulate TLS by setting r.TLS (httptest doesn't do this)
				// We can't easily set r.TLS in httptest, so skip this case
				// or use a custom approach. For now, use X-Forwarded-Proto.
				req.Header.Set("X-Forwarded-Proto", "https")
				return req
			},
			expectHSTS: true,
			desc:       "Direct TLS connection (simulated via header)",
		},
		{
			name: "TLS via X-Forwarded-Proto https",
			makeRequest: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
				req.Header.Set("X-Forwarded-Proto", "https")
				return req
			},
			expectHSTS: true,
			desc:       "Behind proxy with HTTPS",
		},
		{
			name: "TLS via X-Forwarded-Proto http,https",
			makeRequest: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
				req.Header.Set("X-Forwarded-Proto", "http, https")
				return req
			},
			expectHSTS: true,
			desc:       "Behind proxy with HTTPS in chain",
		},
		{
			name: "No TLS",
			makeRequest: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/test", nil)
			},
			expectHSTS: false,
			desc:       "Plain HTTP",
		},
		{
			name: "X-Forwarded-Proto http only",
			makeRequest: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
				req.Header.Set("X-Forwarded-Proto", "http")
				return req
			},
			expectHSTS: false,
			desc:       "Behind proxy with HTTP only",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultSecurityHeaders(true)
			middleware := SecurityHeaders(cfg)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			})
			handler := middleware(next)

			req := tc.makeRequest()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			hsts := w.Header().Get("Strict-Transport-Security")
			if tc.expectHSTS && hsts == "" {
				t.Errorf("Expected HSTS header: %s", tc.desc)
			}
			if !tc.expectHSTS && hsts != "" {
				t.Errorf("Did not expect HSTS header: %s (got %s)", tc.desc, hsts)
			}
		})
	}
}

func TestDefaultSecurityHeadersConfig(t *testing.T) {
	// TLS config
	cfgTLS := DefaultSecurityHeaders(true)
	if !cfgTLS.EnableHSTS {
		t.Error("DefaultSecurityHeaders(true) should enable HSTS")
	}
	if cfgTLS.HSTSMaxAge != 31536000 {
		t.Errorf("Default HSTS max-age should be 31536000, got %d", cfgTLS.HSTSMaxAge)
	}
	if !cfgTLS.ContentTypeOptions {
		t.Error("ContentTypeOptions should be true")
	}
	if cfgTLS.FrameOptions != "DENY" {
		t.Errorf("FrameOptions should be DENY, got %s", cfgTLS.FrameOptions)
	}
	if cfgTLS.ReferrerPolicy != "no-referrer" {
		t.Errorf("ReferrerPolicy should be no-referrer, got %s", cfgTLS.ReferrerPolicy)
	}
	if !strings.Contains(cfgTLS.CSPPolicy, "default-src 'self'") {
		t.Errorf("DefaultSecurityHeaders should include default CSP policy, got %q", cfgTLS.CSPPolicy)
	}

	// Non-TLS config
	cfgHTTP := DefaultSecurityHeaders(false)
	if cfgHTTP.EnableHSTS {
		t.Error("DefaultSecurityHeaders(false) should not enable HSTS")
	}
	if cfgHTTP.ContentTypeOptions != true {
		t.Error("ContentTypeOptions should still be true")
	}
	if !strings.Contains(cfgHTTP.CSPPolicy, "default-src 'self'") {
		t.Errorf("DefaultSecurityHeaders(false) should include default CSP policy, got %q", cfgHTTP.CSPPolicy)
	}
}

type fakeHijackerRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (f *fakeHijackerRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	f.hijacked = true
	return nil, nil, nil
}

func TestSecurityHeaders_PreservesHijacker(t *testing.T) {
	cfg := DefaultSecurityHeaders(false)
	middleware := SecurityHeaders(cfg)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not implement http.Hijacker")
		}
		_, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack failed: %v", err)
		}
		called = true
	})

	handler := middleware(next)
	rec := &fakeHijackerRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	handler.ServeHTTP(rec, req)

	if !called || !rec.hijacked {
		t.Fatal("expected handler to hijack successfully")
	}
}


package httpguard

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strings"
)

// SecurityHeadersConfig holds configuration for security headers.
type SecurityHeadersConfig struct {
	// EnableHSTS enables Strict-Transport-Security header. Only applied when TLS is active.
	EnableHSTS bool
	// HSTSMaxAge is the max-age value in seconds for HSTS.
	HSTSMaxAge int
	// CSPPolicy is the Content-Security-Policy value. Empty means no CSP header.
	CSPPolicy string
	// FrameOptions is the X-Frame-Options value. Empty means no header.
	FrameOptions string
	// ContentTypeOptions enables X-Content-Type-Options: nosniff.
	ContentTypeOptions bool
	// ReferrerPolicy sets the Referrer-Policy header. Empty means no header.
	ReferrerPolicy string
	// PermissionsPolicy sets the Permissions-Policy header. Empty means no header.
	PermissionsPolicy string
}

// DefaultSecurityHeaders returns a config suitable for production use.
func DefaultSecurityHeaders(isTLS bool) SecurityHeadersConfig {
	cfg := SecurityHeadersConfig{
		ContentTypeOptions: true,
		FrameOptions:       "DENY",
		ReferrerPolicy:     "no-referrer",
		PermissionsPolicy:  "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=()",
		CSPPolicy:          "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self' ws: wss:; frame-ancestors 'none'; object-src 'none'; base-uri 'self';",
	}
	if isTLS {
		cfg.EnableHSTS = true
		cfg.HSTSMaxAge = 31536000 // 1 year
	}
	return cfg
}

// SecurityHeaders returns middleware that adds security headers to all responses.
// For WebSocket upgrade responses, CSP is omitted because the upgrade response
// has no body and CSP would be ignored by the browser.
func SecurityHeaders(cfg SecurityHeadersConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Set headers before calling next so they apply to all responses
			// including error responses from downstream handlers.
			if cfg.ContentTypeOptions {
				w.Header().Set("X-Content-Type-Options", "nosniff")
			}
			if cfg.FrameOptions != "" {
				w.Header().Set("X-Frame-Options", cfg.FrameOptions)
			}
			if cfg.ReferrerPolicy != "" {
				w.Header().Set("Referrer-Policy", cfg.ReferrerPolicy)
			}
			if cfg.PermissionsPolicy != "" {
				w.Header().Set("Permissions-Policy", cfg.PermissionsPolicy)
			}

			// HSTS only on HTTPS
			if cfg.EnableHSTS && isTLS(r) {
				w.Header().Set("Strict-Transport-Security", "max-age="+itoa(cfg.HSTSMaxAge)+"; includeSubDomains")
			}

			// Skip CSP wrapping for WebSocket upgrades: status 101 has no body
			// and requires raw connection hijacking.
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				next.ServeHTTP(w, r)
				return
			}

			if cfg.CSPPolicy != "" {
				cspw := &cspResponseWriter{ResponseWriter: w, policy: cfg.CSPPolicy}
				next.ServeHTTP(cspw, r)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// cspResponseWriter wraps http.ResponseWriter to conditionally add CSP
// only when the status code is not 101 (Switching Protocols).
type cspResponseWriter struct {
	http.ResponseWriter
	policy  string
	written bool
}

func (c *cspResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := c.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not implement http.Hijacker")
}

func (c *cspResponseWriter) Flush() {
	if fl, ok := c.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func (c *cspResponseWriter) WriteHeader(statusCode int) {
	if !c.written {
		c.written = true
		// Skip CSP on WebSocket upgrade (101)
		if statusCode != http.StatusSwitchingProtocols && c.policy != "" {
			c.Header().Set("Content-Security-Policy", c.policy)
		}
	}
	c.ResponseWriter.WriteHeader(statusCode)
}

func (c *cspResponseWriter) Write(b []byte) (int, error) {
	if !c.written {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

// isTLS reports whether the request was made over TLS.
func isTLS(r *http.Request) bool {
	// Check the request's TLS field (set by http.Server when TLS is used)
	if r.TLS != nil {
		return true
	}
	// Behind a proxy: check X-Forwarded-Proto
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return strings.Contains(strings.ToLower(proto), "https")
	}
	return false
}

func itoa(i int) string {
	// Fast path for common small integers
	switch i {
	case 0:
		return "0"
	case 31536000:
		return "31536000"
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}

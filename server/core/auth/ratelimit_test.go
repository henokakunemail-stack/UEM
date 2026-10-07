package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPRateLimiter_LockoutAndRecovery(t *testing.T) {
	// 3 max attempts within 200ms, lockout for 300ms
	limiter := NewIPRateLimiter(3, 200*time.Millisecond, 300*time.Millisecond)
	defer limiter.Close()

	ip := "192.168.1.100"

	// Initially allowed
	if allowed, _ := limiter.IsAllowed(ip); !allowed {
		t.Fatal("expected IP to be initially allowed")
	}

	// Record 2 failures -> should still be allowed
	limiter.RecordFailure(ip)
	limiter.RecordFailure(ip)
	if allowed, _ := limiter.IsAllowed(ip); !allowed {
		t.Fatal("expected IP to still be allowed after 2 failures")
	}

	// 3rd failure -> locks out
	limiter.RecordFailure(ip)
	allowed, remaining := limiter.IsAllowed(ip)
	if allowed {
		t.Fatal("expected IP to be locked out after 3 failures")
	}
	if remaining <= 0 {
		t.Fatalf("expected positive remaining lockout time, got %v", remaining)
	}

	// Another IP is not locked out
	if allowed, _ := limiter.IsAllowed("192.168.1.101"); !allowed {
		t.Fatal("unrelated IP should not be locked out")
	}

	// Wait for lockout to expire
	time.Sleep(350 * time.Millisecond)
	if allowed, _ := limiter.IsAllowed(ip); !allowed {
		t.Fatal("expected IP to be allowed after lockout expired")
	}
}

func TestIPRateLimiter_SuccessResets(t *testing.T) {
	limiter := NewIPRateLimiter(3, 1*time.Minute, 1*time.Minute)
	defer limiter.Close()

	ip := "10.0.0.50"
	limiter.RecordFailure(ip)
	limiter.RecordFailure(ip)

	// Record success
	limiter.RecordSuccess(ip)

	// Should allow 2 more failures without locking out
	limiter.RecordFailure(ip)
	limiter.RecordFailure(ip)
	if allowed, _ := limiter.IsAllowed(ip); !allowed {
		t.Fatal("expected IP to be allowed after reset")
	}
}

func TestValidateWebSocketOrigin(t *testing.T) {
	tests := []struct {
		name    string
		origin  string
		host    string
		allowed bool
	}{
		{
			name:    "empty origin (native agent / CLI)",
			origin:  "",
			host:    "mgmt.example.com",
			allowed: true,
		},
		{
			name:    "localhost origin",
			origin:  "http://localhost:8443",
			host:    "localhost:8443",
			allowed: true,
		},
		{
			name:    "loopback 127.0.0.1 origin",
			origin:  "http://127.0.0.1:5173",
			host:    "127.0.0.1:8443",
			allowed: true,
		},
		{
			name:    "same host origin",
			origin:  "https://corp-mgmt.internal:8443",
			host:    "corp-mgmt.internal:8443",
			allowed: true,
		},
		{
			name:    "unconfigured domain is rejected by default",
			origin:  "https://console.example.com",
			host:    "mgmt.example.com",
			allowed: false,
		},
		{
			name:    "malicious external origin",
			origin:  "https://evil-attacker.com",
			host:    "mgmt.example.com",
			allowed: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			got := ValidateWebSocketOrigin(req)
			if got != tc.allowed {
				t.Errorf("ValidateWebSocketOrigin(%q, host=%q) = %v, want %v", tc.origin, tc.host, got, tc.allowed)
			}
		})
	}
}

// TestNewOriginChecker covers the operator-configured domain list, including
// the spoofing shapes that a naive suffix check would wave through.
func TestNewOriginChecker(t *testing.T) {
	check := NewOriginChecker([]string{"console.example.com", "*.corp.example.org"})

	tests := []struct {
		name    string
		origin  string
		host    string
		allowed bool
	}{
		{
			name:    "configured exact domain",
			origin:  "https://console.example.com",
			host:    "mgmt.example.com",
			allowed: true,
		},
		{
			name:    "configured wildcard subdomain",
			origin:  "https://ops.corp.example.org",
			host:    "mgmt.example.com",
			allowed: true,
		},
		{
			name:    "wildcard does not match the bare apex",
			origin:  "https://corp.example.org",
			host:    "mgmt.example.com",
			allowed: false,
		},
		{
			name:    "configured exact domain does not match a subdomain",
			origin:  "https://evil.console.example.com",
			host:    "mgmt.example.com",
			allowed: false,
		},
		{
			name:    "suffix spoofing against wildcard entry",
			origin:  "https://corp.example.org.attacker.com",
			host:    "mgmt.example.com",
			allowed: false,
		},
		{
			name:    "unrelated domain",
			origin:  "https://attacker.com",
			host:    "mgmt.example.com",
			allowed: false,
		},
		{
			name:    "case insensitive match",
			origin:  "https://CONSOLE.Example.COM",
			host:    "mgmt.example.com",
			allowed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			req.Host = tc.host
			req.Header.Set("Origin", tc.origin)
			if got := check(req); got != tc.allowed {
				t.Errorf("check(%q, host=%q) = %v, want %v", tc.origin, tc.host, got, tc.allowed)
			}
		})
	}
}

// TestNewOriginChecker_EmptyListTrustsNothingExtra documents the safe default:
// with no configuration the checker only allows loopback and same-host.
func TestNewOriginChecker_EmptyListTrustsNothingExtra(t *testing.T) {
	check := NewOriginChecker(nil)
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Host = "mgmt.example.com"
	req.Header.Set("Origin", "https://anything.example.org")
	if check(req) {
		t.Error("empty allow-list accepted a foreign origin")
	}
}

// TestClientIP covers the three cases the resolver has to get right. The
// untrusted-header case is the one that used to be a security hole: the old
// ClientIP read X-Forwarded-For unconditionally, so a brute-force run that set
// a fresh value on every request saw every attempt as a new address and the
// lockout never armed.
func TestClientIP(t *testing.T) {
	// 10.0.0.1 is not loopback and is not in the list, so its headers are ignored.
	resolver := NewClientIPResolver([]string{"192.0.2.0/24", "198.51.100.7"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"

	if ip := resolver.ClientIP(req); ip != "10.0.0.1" {
		t.Fatalf("expected the peer address 10.0.0.1, got %q", ip)
	}

	// An untrusted peer's forwarded header is not consulted at all.
	req.Header.Set("X-Forwarded-For", "203.0.113.195, 70.41.3.18")
	if ip := resolver.ClientIP(req); ip != "10.0.0.1" {
		t.Fatalf("an untrusted peer's X-Forwarded-For was honoured: got %q, "+
			"want the peer address 10.0.0.1", ip)
	}

	// A trusted CIDR: the header is read, and the first entry wins.
	trusted := httptest.NewRequest(http.MethodGet, "/", nil)
	trusted.RemoteAddr = "192.0.2.99:12345"
	trusted.Header.Set("X-Forwarded-For", "203.0.113.195, 70.41.3.18")
	if ip := resolver.ClientIP(trusted); ip != "203.0.113.195" {
		t.Fatalf("a trusted proxy's X-Forwarded-For was not honoured: got %q, "+
			"want 203.0.113.195", ip)
	}

	// A trusted exact IP, and X-Real-IP instead of X-Forwarded-For.
	trustedExact := httptest.NewRequest(http.MethodGet, "/", nil)
	trustedExact.RemoteAddr = "198.51.100.7:12345"
	trustedExact.Header.Set("X-Real-IP", "198.51.100.42")
	if ip := resolver.ClientIP(trustedExact); ip != "198.51.100.42" {
		t.Fatalf("a trusted proxy's X-Real-IP was not honoured: got %q, "+
			"want 198.51.100.42", ip)
	}
}

// TestLoopbackIsAlwaysTrusted: a single-box deployment puts a proxy on the same
// host as the server, and TRUSTED_PROXIES is empty in that setup by default.
// Loopback has to keep working, or the default configuration rate-limits the
// console by treating every request as coming from an untrusted peer.
func TestLoopbackIsAlwaysTrusted(t *testing.T) {
	resolver := NewClientIPResolver(nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.195")
	if ip := resolver.ClientIP(req); ip != "203.0.113.195" {
		t.Fatalf("loopback without TRUSTED_PROXIES did not honour the header: "+
			"got %q, want 203.0.113.195", ip)
	}
}

// TestMalformedTrustedProxyEntriesAreSkipped: one bad entry in TRUSTED_PROXIES
// is one misconfigured address. Refusing to start over it would turn a typo
// into an outage, so the bad entry is dropped and the good ones still apply.
func TestMalformedTrustedProxyEntriesAreSkipped(t *testing.T) {
	resolver := NewClientIPResolver([]string{"not-an-ip", "192.0.2.0/24"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.5:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.195")
	if ip := resolver.ClientIP(req); ip != "203.0.113.195" {
		t.Fatalf("a valid entry after a malformed one was not honoured: got %q, "+
			"want 203.0.113.195", ip)
	}
}

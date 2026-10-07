package auth

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

type ipRecord struct {
	count       int
	firstSeen   time.Time
	lockedUntil time.Time
}

// IPRateLimiter provides sliding-window brute force protection on sensitive endpoints like login.
type IPRateLimiter struct {
	maxAttempts int
	window      time.Duration
	lockout     time.Duration
	records     map[string]*ipRecord
	mu          sync.Mutex
	stop        chan struct{}
}

// NewIPRateLimiter creates a new rate limiter with background cleanup.
func NewIPRateLimiter(maxAttempts int, window, lockout time.Duration) *IPRateLimiter {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if window <= 0 {
		window = 1 * time.Minute
	}
	if lockout <= 0 {
		lockout = 5 * time.Minute
	}
	limiter := &IPRateLimiter{
		maxAttempts: maxAttempts,
		window:      window,
		lockout:     lockout,
		records:     make(map[string]*ipRecord),
		stop:        make(chan struct{}),
	}
	go limiter.cleanupLoop()
	return limiter
}

// IsAllowed checks whether the IP is allowed to attempt authentication.
// Returns false and remaining lockout duration if locked.
func (l *IPRateLimiter) IsAllowed(ip string) (bool, time.Duration) {
	if l == nil || ip == "" {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	rec, exists := l.records[ip]
	if !exists {
		return true, 0
	}

	now := time.Now().UTC()
	if !rec.lockedUntil.IsZero() {
		if now.Before(rec.lockedUntil) {
			return false, rec.lockedUntil.Sub(now)
		}
		// Lockout expired, reset record
		delete(l.records, ip)
		return true, 0
	}

	return true, 0
}

// RecordFailure records a failed authentication attempt.
func (l *IPRateLimiter) RecordFailure(ip string) {
	if l == nil || ip == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UTC()
	rec, exists := l.records[ip]
	if !exists {
		l.records[ip] = &ipRecord{
			count:     1,
			firstSeen: now,
		}
		return
	}

	// If window expired without locking, restart window
	if now.Sub(rec.firstSeen) > l.window {
		rec.count = 1
		rec.firstSeen = now
		rec.lockedUntil = time.Time{}
		return
	}

	rec.count++
	if rec.count >= l.maxAttempts {
		rec.lockedUntil = now.Add(l.lockout)
	}
}

// RecordSuccess clears any failure records for the IP upon successful login.
func (l *IPRateLimiter) RecordSuccess(ip string) {
	if l == nil || ip == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.records, ip)
}

// Close stops the background cleaner goroutine.
func (l *IPRateLimiter) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	select {
	case <-l.stop:
		l.mu.Unlock()
		return
	default:
		close(l.stop)
	}
	l.mu.Unlock()
}

func (l *IPRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.cleanup()
		case <-l.stop:
			return
		}
	}
}

func (l *IPRateLimiter) cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UTC()
	for ip, rec := range l.records {
		if !rec.lockedUntil.IsZero() {
			if now.After(rec.lockedUntil) {
				delete(l.records, ip)
			}
		} else if now.Sub(rec.firstSeen) > l.window*2 {
			delete(l.records, ip)
		}
	}
}

// ClientIPResolver reads the caller's address, honouring the forwarding headers
// a reverse proxy sets but only when the connection came from a proxy this
// deployment trusts.
type ClientIPResolver struct {
	// trusted holds the parsed proxy entries, each an IP or a CIDR. Loopback is
	// always in it: the console and any sidecar on the same host are trusted by
	// construction, and refusing them would break a single-binary deployment
	// that puts a proxy in front of itself on the same box.
	trusted []*net.IPNet
}

// NewClientIPResolver builds a resolver. Entries that fail to parse are skipped
// with a warning rather than aborting the server: one malformed TRUSTED_PROXIES
// entry is a misconfiguration of one address, not a reason to refuse to start,
// and the rest of the list still works.
//
// A hostname is resolved once, here, rather than on every request. DNS is not
// part of the request path, and a compose file that names the proxy service
// ("caddy") should work the same way an IP does.
func NewClientIPResolver(trustedProxies []string) *ClientIPResolver {
	r := &ClientIPResolver{trusted: []*net.IPNet{}}
	// Loopback is always trusted. The single-binary deployment puts the console
	// and any sidecar proxy on the same host as the server, and an operator who
	// has not set TRUSTED_PROXIES still needs those to work; the host itself is
	// not an attacker.
	for _, cidr := range []string{"127.0.0.0/8", "::1/128"} {
		if n, err := parseTrustedProxy(cidr); err == nil {
			r.trusted = append(r.trusted, n)
		}
	}
	for _, cidr := range trustedProxies {
		if n, err := parseTrustedProxy(cidr); err == nil {
			r.trusted = append(r.trusted, n)
		} else {
			log.Warn().Err(err).Str("entry", cidr).
				Msg("TRUSTED_PROXIES entry is not an IP or CIDR; ignoring it")
		}
	}
	return r
}

func parseTrustedProxy(entry string) (*net.IPNet, error) {
	if strings.Contains(entry, "/") {
		_, n, err := net.ParseCIDR(entry)
		return n, err
	}
	ip := net.ParseIP(entry)
	if ip == nil {
		// A service name, as a compose file would write it. Resolved once at
		// startup; a name that does not resolve is a misconfiguration of that
		// one entry, not a startup failure.
		ips, err := net.LookupIP(entry)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("%q is not an IP address or a resolvable name: %w", entry, err)
		}
		ip = ips[0]
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
}

// isTrusted reports whether the peer that opened the connection is a proxy the
// operator vouched for. It is a CIDR membership test, so a deployment behind a
// whole internal subnet does not have to list load-balancer replicas one at a
// time.
func (r *ClientIPResolver) isTrusted(peer string) bool {
	ip := net.ParseIP(peer)
	if ip == nil {
		return false
	}
	for _, n := range r.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the address the request came from. The forwarded headers are
// read only when the connection itself is trusted; from anybody else the peer
// address is the answer, because that is the one thing the remote end cannot
// choose for itself.
//
// This is the difference between a rate limiter that resists a brute-force
// attempt and one that does not. Without the trust gate, an attacker sets a
// fresh X-Forwarded-For on every request and every attempt comes from a "new"
// address, so the lockout never arms. The headers are useful only behind a
// proxy, and a proxy's address is exactly what TRUSTED_PROXIES is for.
func (r *ClientIPResolver) ClientIP(rq *http.Request) string {
	if host, _, err := net.SplitHostPort(rq.RemoteAddr); err == nil {
		if r.isTrusted(host) {
			if ip := r.forwardedFor(rq); ip != "" {
				return ip
			}
		}
	} else if net.ParseIP(rq.RemoteAddr) != nil && r.isTrusted(rq.RemoteAddr) {
		if ip := r.forwardedFor(rq); ip != "" {
			return ip
		}
	}
	return remoteHost(rq.RemoteAddr)
}

// forwardedFor takes the first address in the list, which is the one the
// trusted proxy saw as the original client. A proxy appends to the right, so
// entries to its left are progressively more remote and entries to its right
// are what the request itself carried -- the ones an untrusted hop would have
// written.
func (r *ClientIPResolver) forwardedFor(rq *http.Request) string {
	if xff := rq.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		ip := strings.TrimSpace(parts[0])
		if ip != "" {
			return ip
		}
	}
	if xri := rq.Header.Get("X-Real-IP"); xri != "" {
		ip := strings.TrimSpace(xri)
		if ip != "" {
			return ip
		}
	}
	return ""
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil && host != "" {
		return host
	}
	return remoteAddr
}

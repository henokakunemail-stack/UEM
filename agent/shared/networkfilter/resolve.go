package networkfilter

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// A hosts file cannot express a subdomain, so the hosts layer only ever blocks the
// exact name that was written. Resolving the domain to its addresses lets the
// firewall layer block everything that reaches the same server -- every subdomain,
// and a browser using its own encrypted resolver, which the hosts file cannot
// touch at all.
//
// Addresses are cached for an hour. That bounds how long a policy change takes to
// fully apply if a site is re-pointed, without needing a per-domain TTL the
// platform does not expose.
const resolveCacheTTL = time.Hour

type resolveEntry struct {
	addrs []net.IP
	until time.Time
}

type resolver struct {
	mu    sync.Mutex
	cache map[string]resolveEntry
	// now is swapped in tests so expiry can be exercised without sleeping.
	now func() time.Time
}

func newResolver() *resolver {
	return &resolver{cache: make(map[string]resolveEntry), now: time.Now}
}

// normalizeDomain strips what an operator might paste from a browser address bar.
// The hosts layer already does this, but the firewall layer resolves the name, and
// net.LookupIP rejects "https://news.detik.com/abc" outright -- so the same
// cleanup has to happen before the lookup or the rule silently blocks nothing.
func normalizeDomain(in string) string {
	d := strings.TrimSpace(in)
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	d = strings.TrimPrefix(d, "*.")
	// A hosts entry may carry a port or a path; the name is what resolves.
	if i := strings.IndexAny(d, "/:"); i >= 0 {
		d = d[:i]
	}
	return strings.ToLower(strings.TrimSuffix(d, "."))
}

func (r *resolver) resolve(domain string) ([]net.IP, error) {
	d := normalizeDomain(domain)
	if d == "" {
		return nil, fmt.Errorf("empty domain")
	}

	r.mu.Lock()
	if hit, ok := r.cache[d]; ok && r.now().Before(hit.until) {
		r.mu.Unlock()
		return hit.addrs, nil
	}
	r.mu.Unlock()

	ips, err := net.LookupIP(d)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", d, err)
	}
	var validIPs []net.IP
	for _, ip := range ips {
		// Drop sinkhole / loopback addresses (0.0.0.0, 127.0.0.1, ::) so the firewall
		// never installs rules for local addresses if a hosts file was touched.
		if !ip.IsUnspecified() && !ip.IsLoopback() {
			validIPs = append(validIPs, ip)
		}
	}
	if len(validIPs) == 0 {
		return nil, fmt.Errorf("resolve %s: no valid addresses", d)
	}

	r.mu.Lock()
	r.cache[d] = resolveEntry{addrs: validIPs, until: r.now().Add(resolveCacheTTL)}
	r.mu.Unlock()
	return validIPs, nil
}

// resolveAll returns the deduplicated union of every domain's addresses. One
// domain that fails to resolve is logged by the caller and skipped rather than
// failing the batch: a typo in one rule should not cost the operator the other
// forty.
func (r *resolver) resolveAll(domains []string) ([]net.IP, map[string]string) {
	var all []net.IP
	seen := make(map[string]bool)
	errs := make(map[string]string)
	for _, d := range domains {
		addrs, err := r.resolve(d)
		if err != nil {
			errs[normalizeDomain(d)] = err.Error()
			continue
		}
		for _, ip := range addrs {
			if s := ip.String(); !seen[s] {
				seen[s] = true
				all = append(all, ip)
			}
		}
	}
	return all, errs
}

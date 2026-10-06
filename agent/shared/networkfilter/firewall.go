package networkfilter

import (
	"errors"
	"fmt"
	"net"
)

// ErrFirewallUnavailable means this platform has no firewall backend compiled in
// for the current build. It is a distinct error from ErrFirewallPermission so the
// caller can tell "this agent was built without firewall support" from "this agent
// is not running as administrator" -- the second is an operator-fixable problem
// and the first is not.
var ErrFirewallUnavailable = errors.New("networkfilter: firewall backend unavailable")

// ErrFirewallPermission means a backend exists for this platform but the OS
// refused the change, almost always because the agent is not running elevated.
var ErrFirewallPermission = errors.New("networkfilter: firewall change was refused by the OS")

// firewallBackend blocks a set of addresses at the network layer.
//
// Contract: Apply is idempotent and total. Every call first removes everything
// this agent installed, then installs exactly the addresses passed in. A caller
// that passes an empty slice therefore clears enforcement completely -- which is
// what disabling a policy has to do, and what a hosts-file rewrite alone cannot
// achieve once the firewall layer holds state of its own.
type firewallBackend interface {
	// Supported reports whether this platform has a backend at all.
	Supported() bool
	// Apply replaces the managed block set with addrs. It returns the number of
	// addresses actually enforced, which can be lower than len(addrs) if some were
	// rejected as already-present or duplicate.
	Apply(addrs []net.IP) (int, error)
}

// newFirewallBackend is defined per platform, in a build-tagged file next to the
// backend it returns. Referencing every backend type from one shared file does not
// compile: a backend's type is absent from a build whose build constraints
// excluded its file.

// applyFirewall resolves the domains and pushes their addresses into the platform
// firewall, returning how many were enforced plus any domains that failed to
// resolve. A caller wanting to know whether the firewall layer actually came up
// must check the error, not the count: a count of zero with a nil error means the
// policy legitimately resolved to nothing.
func applyFirewall(fb firewallBackend, rs *resolver, domains []string) (int, map[string]string, error) {
	if !fb.Supported() {
		return 0, nil, ErrFirewallUnavailable
	}
	addrs, resolveErrs := rs.resolveAll(domains)
	if len(addrs) == 0 {
		// Still call Apply with an empty set. A domain that used to resolve and no
		// longer does must leave its old addresses blocked rather than lingering
		// until the next policy change.
		if _, err := fb.Apply(nil); err != nil {
			return 0, resolveErrs, err
		}
		return 0, resolveErrs, nil
	}
	n, err := fb.Apply(addrs)
	if err != nil {
		return 0, resolveErrs, err
	}
	return n, resolveErrs, nil
}

func describeFirewallError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrFirewallUnavailable):
		return "no firewall backend for this platform; only host-file blocking is active"
	case errors.Is(err, ErrFirewallPermission):
		return "the OS refused the firewall change; the agent is probably not running as administrator, so only host-file blocking is active"
	default:
		return fmt.Sprintf("firewall: %v", err)
	}
}

// chunkIPs splits a list so each platform CLI call stays under its command-line
// limit. Windows and Linux differ only in the chunk size they pass in, so the
// split lives here rather than being written twice.
func chunkIPs(all []net.IP, size int) [][]net.IP {
	var chunks [][]net.IP
	for i := 0; i < len(all); i += size {
		end := i + size
		if end > len(all) {
			end = len(all)
		}
		chunks = append(chunks, all[i:end])
	}
	return chunks
}

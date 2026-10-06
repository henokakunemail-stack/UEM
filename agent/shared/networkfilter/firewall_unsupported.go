package networkfilter

import "net"

// unsupportedFirewall stands in for platforms with no backend written yet, and
// is the reference implementation of the interface's contract: Supported() false,
// Apply returning ErrFirewallUnavailable.
//
// It carries no build tag so the type exists on every platform and the engine
// tests can exercise the absent-backend branch wherever they run. A test that only
// ran where the backend was missing would never run in CI, because CI is on
// Linux, which has one. Only the constructor is platform-specific -- see
// newFirewallBackend in firewall_nobackend.go.
type unsupportedFirewall struct{}

func (u *unsupportedFirewall) Supported() bool { return false }

func (u *unsupportedFirewall) Apply([]net.IP) (int, error) {
	return 0, ErrFirewallUnavailable
}
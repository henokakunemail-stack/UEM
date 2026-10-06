//go:build !windows && !linux

package networkfilter

// newFirewallBackend for platforms with no backend of their own. Windows and Linux
// define theirs inside their build-tagged file instead -- a shared file cannot
// reference all three, because a backend's type is absent from any build that
// excluded its file.
//
// On macOS this is what executes, so enforcement falls back to the hosts file and
// reports itself degraded, rather than claiming a firewall it never touched.
func newFirewallBackend() firewallBackend { return &unsupportedFirewall{} }
//go:build windows

package networkfilter

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// ruleGroup tags every rule this agent installs, so Apply can find and remove
// exactly its own rules and nothing else on the machine.
//
// The tag matters: deleting by name would also catch a rule an administrator made
// by hand with a similar name, and deleting by program path would catch the
// Windows firewall's own rules. A group is the only handle that is specific to
// this agent.
const ruleGroup = "EndpointManager-Filter"

// maxNetshArgs bounds one netsh invocation. netsh parses the whole command line at
// once, and a policy with a few thousand addresses will exceed the Windows 32k
// command-line limit -- which surfaces as a generic "The filename or extension is
// too long" rather than anything that identifies the real cause. Chunking also
// keeps each round trip short enough that a large policy does not stall the agent's
// dispatch loop.
const maxNetshArgs = 60

type windowsFirewall struct{}

func newFirewallBackend() firewallBackend { return &windowsFirewall{} }

func (w *windowsFirewall) Supported() bool {
	_, err := exec.LookPath("netsh")
	return err == nil
}

// netsh runs a command and surfaces the real error. Exit status alone is not
// enough: netsh prints its failure to stderr and exits 0 in several cases.
func netsh(args ...string) error {
	var stderr strings.Builder
	cmd := exec.Command("netsh", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

func (w *windowsFirewall) Apply(addrs []net.IP) (int, error) {
	// Delete first, unconditionally. Applying an empty set has to clear the
	// previous one, and doing the delete unconditionally means Apply converges on
	// the requested set whether the previous call succeeded, failed partway, or
	// never ran.
	if err := netsh("advfirewall", "firewall", "delete", "rule", "group="+ruleGroup); err != nil {
		// No rules to delete is the normal first-run case, not a failure. netsh
		// reports it on stderr with a zero exit status, so only treat a genuinely
		// empty result as success.
		if !strings.Contains(strings.ToLower(err.Error()), "no rules match") &&
			!strings.Contains(strings.ToLower(err.Error()), "no rules") {
			return 0, fmt.Errorf("%w: %v", ErrFirewallPermission, err)
		}
	}
	if len(addrs) == 0 {
		return 0, nil
	}

	applied := 0
	for _, chunk := range chunkIPs(addrs, maxNetshArgs) {
		args := []string{
			"advfirewall", "firewall", "add", "rule",
			"name=" + ruleGroup,
			"dir=out",
			"action=block",
			"group=" + ruleGroup,
			// The engine block is not a single process: a browser, an updater and
			// a service all have to be stopped, so scoping by program would miss
			// most of the traffic.
			"protocol=any",
			"interfacetype=any",
			"remoteip=" + joinIPs(chunk),
		}
		if err := netsh(args...); err != nil {
			return applied, fmt.Errorf("%w: %v", ErrFirewallPermission, err)
		}
		applied += len(chunk)
	}
	return applied, nil
}

// joinIPs renders a chunk for netsh's remoteip parameter, which takes a
// comma-separated list and rejects the IPv4-mapped IPv6 form.
func joinIPs(addrs []net.IP) string {
	parts := make([]string, len(addrs))
	for i, ip := range addrs {
		// IPv4-mapped IPv6 results from LookupIP on a dual-stack host have to be
		// written as plain IPv4. netsh rejects "::ffff:1.2.3.4" in remoteip, which
		// would fail the whole chunk rather than skipping one address.
		if v4 := ip.To4(); v4 != nil {
			parts[i] = v4.String()
		} else {
			parts[i] = ip.String()
		}
	}
	return strings.Join(parts, ",")
}

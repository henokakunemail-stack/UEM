//go:build linux

package networkfilter

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

// table and set names live in their own nftables table so cleanup can delete the
// whole thing in one call, and so nothing else on the machine can collide with
// them.
const (
	nftTable = "endpoint_manager"
	nftSet   = "blocklist"
)

// maxNftArgs bounds one nft invocation for the same reason as the Windows chunking:
// a policy with thousands of addresses would otherwise build a command line the
// kernel cannot exec.
const maxNftArgs = 200

type nftablesFirewall struct{}

func newFirewallBackend() firewallBackend { return &nftablesFirewall{} }

func (n *nftablesFirewall) Supported() bool {
	_, err := exec.LookPath("nft")
	return err == nil
}

func runNFT(args ...string) error {
	var stderr strings.Builder
	cmd := exec.Command("nft", args...)
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

func (n *nftablesFirewall) Apply(addrs []net.IP) (int, error) {
	// Replace the whole table. nft has no "clear and reuse" for a set that may not
	// exist yet, and deleting first makes Apply converge: a partial failure
	// leaves the previous table gone rather than merged with the new one.
	if err := runNFT("delete", "table", "inet", nftTable); err != nil {
		// Absent table is the normal first-run case.
		if !isNotFound(err) {
			return 0, fmt.Errorf("%w: %v", ErrFirewallPermission, err)
		}
	}
	if len(addrs) == 0 {
		return 0, nil
	}

	// A set with `flags timeout` drops each address on its own timer, so a site
	// that changes its IP records recovers without the agent being online to push
	// an update. Without this, a stale address would block a server that has since
	// been reassigned to an unrelated host.
	if err := runNFT(
		"add", "table", "inet", nftTable,
		"add", "set", "inet", nftTable, nftSet, "{ type ipv4_addr; flags timeout; }",
		"add", "chain", "inet", nftTable, "output", "{ type filter hook output priority 0; policy accept; }",
		"add", "rule", "inet", nftTable, "output",
		"ip", "daddr", "@"+nftSet, "drop",
	); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrFirewallPermission, err)
	}

	applied := 0
	for _, chunk := range chunkIPs(addrs, maxNftArgs) {
		elements := make([]string, len(chunk))
		for i, ip := range chunk {
			v4 := ip.To4()
			if v4 == nil {
				// The set is declared ipv4_addr. An IPv6 address cannot go in it, and
				// dropping it silently would leave that half of the site's traffic
				// unblocked while the console reported success.
				return applied, fmt.Errorf("address %s is IPv6; this build enforces IPv4 only", ip)
			}
			elements[i] = fmt.Sprintf("%s timeout %ds", v4.String(), int(resolveCacheTTL/time.Second))
		}
		if err := runNFT("add", "element", "inet", nftTable, nftSet,
			"{ "+strings.Join(elements, ", ")+" }"); err != nil {
			return applied, fmt.Errorf("%w: %v", ErrFirewallPermission, err)
		}
		applied += len(chunk)
	}
	return applied, nil
}

// isNotFound reports whether nft complained that the thing it was asked to remove
// was not there, which is success for a delete-then-create sequence.
func isNotFound(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "not found")
}

package networkfilter

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingFirewall stands in for a platform backend so the engine's contract can
// be tested without elevation. It records the exact address set each Apply call
// received, which is the only way to prove the "clear what was there first"
// behaviour -- a real firewall on the developer's machine would make the test
// depend on admin rights and on what the network looked like that day.
//
// Supported is always true so that err can carry a specific failure. Tying the two
// together would make every error look like an absent backend, which is the exact
// distinction the operator has to act on: one is fixed by a rebuild, the other by
// running the agent elevated.
type recordingFirewall struct {
	calls [][]net.IP
	err   error
}

func (f *recordingFirewall) Supported() bool { return true }

func (f *recordingFirewall) Apply(addrs []net.IP) (int, error) {
	f.calls = append(f.calls, addrs)
	if f.err != nil {
		return 0, f.err
	}
	return len(addrs), nil
}

func (f *recordingFirewall) last() []net.IP {
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

func testEngine(t *testing.T, fb firewallBackend) (*Engine, string) {
	t.Helper()
	hosts := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hosts, []byte("127.0.0.1 localhost\n"), 0644); err != nil {
		t.Fatal(err)
	}
	e := NewEngine("http://localhost:8443", "dev-1", "secret")
	e.SetHostsPath(hosts)
	e.firewall = fb
	// Pre-seed the one address every test blocks, so the suite never depends on
	// DNS or on what detik.com resolves to today.
	setResolver(e.resolver, "203.0.113.10")
	return e, hosts
}

// setResolver primes the resolver cache for detik.com so the tests do not touch
// DNS and do not change when a domain's real addresses change.
func setResolver(r *resolver, addrs ...string) {
	setResolverFor(r, "detik.com", addrs...)
}

// setResolverFor primes one domain. Every domain a test blocks must be primed:
// without it the resolver does a real lookup, and a test asserting on address
// counts then depends on how many A records kompas.com happens to have today.
func setResolverFor(r *resolver, domain string, addrs ...string) {
	parsed := make([]net.IP, len(addrs))
	for i, a := range addrs {
		parsed[i] = net.ParseIP(a)
	}
	r.cache[domain] = resolveEntry{addrs: parsed, until: r.now().Add(resolveCacheTTL)}
}

// TestApplyClearsPreviousFirewallRules is the regression that a hosts-only
// implementation could never have.
//
// Apply is total: it replaces whatever it installed before with exactly what it is
// given now. Disabling a policy, or narrowing it to a different device scope, sends
// a shorter list -- and if the firewall layer only ever added rules, the device
// would keep blocking domains no policy mentions any more, with no way to see that
// from the console and no way to undo it but reinstalling the agent.
func TestApplyClearsPreviousFirewallRules(t *testing.T) {
	fw := &recordingFirewall{}
	e, hosts := testEngine(t, fw)

	if _, _, err := e.ApplyBlockedDomains([]string{"detik.com"}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if len(fw.last()) != 1 {
		t.Fatalf("first apply enforced %v, want one address", fw.last())
	}

	if _, _, err := e.ApplyBlockedDomains(nil); err != nil {
		t.Fatalf("apply with an empty policy: %v", err)
	}
	if got := fw.last(); len(got) != 0 {
		t.Fatalf("after disabling the policy the firewall still enforces %v: "+
			"Apply must clear its previous set, not only add to it", got)
	}

	data, err := os.ReadFile(hosts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "detik.com") {
		t.Error("the hosts file still sinkholes detik.com after the policy was cleared")
	}
}

// A narrower policy must replace a wider one rather than merge with it.
func TestApplyNarrowsRatherThanAccumulates(t *testing.T) {
	fw := &recordingFirewall{}
	e, _ := testEngine(t, fw)
	// Both domains primed: one address each, so the union is exactly two and the
	// second apply must leave exactly one.
	setResolver(e.resolver, "203.0.113.10")
	setResolverFor(e.resolver, "kompas.com", "203.0.113.20")

	if _, _, err := e.ApplyBlockedDomains([]string{"detik.com", "kompas.com"}); err != nil {
		t.Fatal(err)
	}
	if got := len(fw.last()); got != 2 {
		t.Fatalf("two domains resolved to %d addresses, want 2", got)
	}

	if _, _, err := e.ApplyBlockedDomains([]string{"detik.com"}); err != nil {
		t.Fatal(err)
	}
	if got := len(fw.last()); got != 1 {
		t.Fatalf("after narrowing to one domain the firewall enforces %d addresses, want 1: "+
			"the dropped domain's addresses are still blocked", got)
	}
}

// The engine must not fail the whole policy when the firewall is unavailable,
// because the hosts layer is the one that works without elevation. But it must say
// so: a device that blocks only via the hosts file still lets www.detik.com through,
// and an operator looking at "synced" would conclude otherwise.
func TestApplySurvivesAnUnavailableFirewallButReportsIt(t *testing.T) {
	fw := &recordingFirewall{err: ErrFirewallPermission}
	e, hosts := testEngine(t, fw)

	count, degraded, err := e.ApplyBlockedDomains([]string{"detik.com"})
	if err != nil {
		t.Fatalf("apply returned an error when only the firewall was unavailable: %v", err)
	}
	if count != 1 {
		t.Errorf("hosts rules applied = %d, want 1: the hosts layer must still run", count)
	}
	if degraded == "" {
		t.Fatal("the refused firewall was reported as a clean apply: the console would " +
			"show the device as fully enforcing when only the hosts file is standing")
	}
	if !strings.Contains(degraded, "administrator") {
		t.Errorf("degraded reason = %q, want it to name the likely cause", degraded)
	}
	data, _ := os.ReadFile(hosts)
	if !strings.Contains(string(data), "0.0.0.0 detik.com") {
		t.Error("the hosts file was not written when the firewall was refused")
	}
}

// A device holding the right rules at both layers must not look degraded, or the
// status stops meaning anything.
func TestCleanApplyIsNotMarkedDegraded(t *testing.T) {
	e, _ := testEngine(t, &recordingFirewall{})

	_, degraded, err := e.ApplyBlockedDomains([]string{"detik.com"})
	if err != nil {
		t.Fatal(err)
	}
	if degraded != "" {
		t.Errorf("a fully enforced apply reported %q as degraded", degraded)
	}
}

// A hosts file that cannot be written means the layer that must always work did
// not. That is a real failure and must not be reported as success.
func TestHostsWriteFailureIsFatal(t *testing.T) {
	fw := &recordingFirewall{}
	e, _ := testEngine(t, fw)
	// Point at a directory: reading it fails before any write is attempted.
	e.SetHostsPath(t.TempDir())

	if _, _, err := e.ApplyBlockedDomains([]string{"detik.com"}); err == nil {
		t.Error("an unreadable hosts file was reported as a successful apply")
	}
	if len(fw.calls) != 0 {
		t.Error("the firewall was touched even though the hosts layer failed")
	}
}

// Patterns arrive from an operator pasting a URL, so the same cleanup the firewall
// layer needs has to happen before the hosts line is written.
func TestNormalizeDomainStripsWhatOperatorsPaste(t *testing.T) {
	for in, want := range map[string]string{
		"detik.com":                        "detik.com",
		"www.detik.com":                    "www.detik.com",
		"https://news.detik.com/teknologi": "news.detik.com",
		"http://detik.com":                 "detik.com",
		"*.detik.com":                      "detik.com",
		"  detik.com  ":                    "detik.com",
		"detik.com.":                       "detik.com",
		"Detik.COM":                        "detik.com",
		"detik.com:443":                    "detik.com",
	} {
		if got := normalizeDomain(in); got != want {
			t.Errorf("normalizeDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

// One unresolvable domain must not cost the operator every other rule in the policy.
func TestOneBadDomainDoesNotDropTheRest(t *testing.T) {
	fw := &recordingFirewall{}
	e, _ := testEngine(t, fw)
	setResolver(e.resolver, "203.0.113.10")

	// Only detik.com is cached, so the second domain performs a real lookup and
	// fails -- which is the case being exercised: one failure, one surviving rule.
	if _, _, err := e.ApplyBlockedDomains([]string{"detik.com", "this-domain-does-not-exist.invalid"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(fw.last()) != 1 {
		t.Fatalf("firewall enforces %v, want the one address that resolved", fw.last())
	}
}

func TestUnsupportedBackendIsDistinguishableFromPermission(t *testing.T) {
	var _ firewallBackend = &unsupportedFirewall{}

	if _, err := (&unsupportedFirewall{}).Apply(nil); !errors.Is(err, ErrFirewallUnavailable) {
		t.Errorf("unsupported backend error = %v, want ErrFirewallUnavailable", err)
	}
	if msg := describeFirewallError(ErrFirewallUnavailable); !strings.Contains(msg, "no firewall backend") {
		t.Errorf("message for an absent backend = %q, want it to say so plainly", msg)
	}
	if msg := describeFirewallError(ErrFirewallPermission); !strings.Contains(msg, "administrator") {
		t.Errorf("message for a refused change = %q, want it to name the likely cause", msg)
	}
}

// chunkIPs is what keeps a large policy from building a command line no OS can
// exec, so the split has to cover every element exactly once.
func TestChunkIPsCoversEverything(t *testing.T) {
	for _, n := range []int{0, 1, 7, 10, 11, 100} {
		all := make([]net.IP, n)
		for i := range all {
			all[i] = net.ParseIP(fmt.Sprintf("203.0.113.%d", i%255))
		}
		var got int
		for _, c := range chunkIPs(all, 10) {
			if len(c) > 10 {
				t.Errorf("%d elements produced a chunk of %d, over the limit", n, len(c))
			}
			got += len(c)
		}
		if got != n {
			t.Errorf("%d elements came back as %d across the chunks", n, got)
		}
	}
}

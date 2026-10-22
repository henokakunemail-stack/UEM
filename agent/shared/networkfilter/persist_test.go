package networkfilter

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDNS stands in for the adapter configuration; nothing on this machine's
// network settings changes.
type fakeDNS struct {
	snapshot    dnsBackup
	snapshotErr error
	pointWarn   string
	pointErr    error
	snapshots   int
	pointed     []dnsBackup
	restored    []dnsBackup
	onPoint     func()
}

func (f *fakeDNS) Snapshot() (dnsBackup, error) {
	f.snapshots++
	return f.snapshot, f.snapshotErr
}
func (f *fakeDNS) PointToLoopback(b dnsBackup) (string, error) {
	if f.onPoint != nil {
		f.onPoint()
	}
	f.pointed = append(f.pointed, b)
	return f.pointWarn, f.pointErr
}
func (f *fakeDNS) Restore(b dnsBackup) error {
	f.restored = append(f.restored, b)
	return nil
}

// fakeProxy records the whitelist it was given instead of binding port 53.
type fakeProxy struct {
	allowed []string
	stopped bool
}

func (p *fakeProxy) UpdateAllowed(a []string) { p.allowed = a }
func (p *fakeProxy) Stop()                    { p.stopped = true }

func persistEngine(t *testing.T) (*Engine, *fakeDNS, *[]*fakeProxy, string) {
	t.Helper()
	e, _ := testEngine(t, &recordingFirewall{})
	dir := t.TempDir()
	e.WithStateDir(dir)
	dns := &fakeDNS{snapshot: dnsBackup{Method: "windows", Adapters: []adapterBackup{{Name: "Ethernet", Addresses: []string{"10.0.0.2"}}}}}
	e.dns = dns
	var proxies []*fakeProxy
	e.startProxy = func(_, _ string, allowed []string, _ string) (whitelistProxy, error) {
		p := &fakeProxy{allowed: allowed}
		proxies = append(proxies, p)
		return p, nil
	}
	e.upstream = func() string { return "10.0.0.2:53" }
	return e, dns, &proxies, dir
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestBackupPersistedBeforeAdapterChange: the backup lived only in memory, so a
// crash after redirecting the adapters left nothing to restore them from.
func TestBackupPersistedBeforeAdapterChange(t *testing.T) {
	e, dns, _, dir := persistEngine(t)
	var onDisk bool
	dns.onPoint = func() {
		_, err := os.Stat(filepath.Join(dir, dnsBackupFile))
		onDisk = err == nil
	}
	if _, _, err := e.ApplyFilter("whitelist", nil, []string{"corp.example"}); err != nil {
		t.Fatal(err)
	}
	if !onDisk {
		t.Error("the adapters were redirected before the DNS backup reached disk")
	}
	e.Close()
	if _, err := os.Stat(filepath.Join(dir, dnsBackupFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the backup was left behind after DNS was restored")
	}
}

// TestRecoverReappliesWhitelistFromPersistedBackup: after a crash the adapters
// still point at 127.0.0.1. The next start must bring the resolver back on top
// of them, using the persisted backup -- reading the live adapters would back up
// 127.0.0.1 and later "restore" them to it.
func TestRecoverReappliesWhitelistFromPersistedBackup(t *testing.T) {
	e, dns, proxies, dir := persistEngine(t)
	writeJSON(t, filepath.Join(dir, filterStateFile), filterState{Version: "v7", Mode: "whitelist", Allowed: []string{"corp.example"}})
	crashed := dnsBackup{Method: "windows", Adapters: []adapterBackup{{Name: "Ethernet", Addresses: []string{"192.168.3.53"}}}}
	writeJSON(t, filepath.Join(dir, dnsBackupFile), crashed)

	rec := e.Recover()
	if rec == nil || rec.Err != nil || rec.Version != "v7" {
		t.Fatalf("recover = %+v", rec)
	}
	if len(*proxies) != 1 || strings.Join((*proxies)[0].allowed, ",") != "corp.example" {
		t.Fatalf("whitelist resolver not restarted with the persisted list: %+v", *proxies)
	}
	if dns.snapshots != 0 || len(dns.restored) != 0 {
		t.Errorf("snapshots=%d restores=%d; the persisted backup must be adopted, not replaced", dns.snapshots, len(dns.restored))
	}
	e.Close()
	if len(dns.restored) != 1 || dns.restored[0].Adapters[0].Addresses[0] != "192.168.3.53" {
		t.Errorf("DNS restored to %+v, want the pre-crash settings", dns.restored)
	}
}

// TestRecoverRestoresDNSWhenNotWhitelist: a backup left by a crash, with no
// whitelist to come back, means the adapters must be put back.
func TestRecoverRestoresDNSWhenNotWhitelist(t *testing.T) {
	e, dns, _, dir := persistEngine(t)
	writeJSON(t, filepath.Join(dir, filterStateFile), filterState{Version: "v8", Mode: "blacklist", Blocked: []string{"detik.com"}})
	writeJSON(t, filepath.Join(dir, dnsBackupFile), dns.snapshot)

	if rec := e.Recover(); rec == nil || rec.Err != nil {
		t.Fatalf("recover = %+v", rec)
	}
	if len(dns.restored) != 1 {
		t.Errorf("%d restores, want the stranded adapters put back once", len(dns.restored))
	}
	if _, err := os.Stat(filepath.Join(dir, dnsBackupFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the stale backup is still on disk")
	}
}

func TestApplyPolicyPersistsForNextStart(t *testing.T) {
	e, _, _, dir := persistEngine(t)
	if _, _, err := e.ApplyPolicy("v9", "blacklist", []string{"detik.com"}, nil); err != nil {
		t.Fatal(err)
	}
	var s filterState
	b, err := os.ReadFile(filepath.Join(dir, filterStateFile))
	if err != nil || json.Unmarshal(b, &s) != nil || s.Version != "v9" || s.Mode != "blacklist" {
		t.Errorf("persisted state = %s (%v)", b, err)
	}
}

// TestWhitelistUnsupportedOSFails: on macOS the adapter step was a no-op and
// the whitelist reported synced while every lookup bypassed it.
func TestWhitelistUnsupportedOSFails(t *testing.T) {
	e, dns, proxies, _ := persistEngine(t)
	dns.snapshotErr = ErrLoopbackUnsupported
	_, _, err := e.ApplyFilter("whitelist", nil, []string{"corp.example"})
	if !errors.Is(err, ErrLoopbackUnsupported) {
		t.Fatalf("err = %v, want ErrLoopbackUnsupported", err)
	}
	if len(*proxies) != 1 || !(*proxies)[0].stopped {
		t.Error("the resolver kept running for a whitelist nothing routes through")
	}
}

// TestPartialAdapterFailureDegraded: a refused adapter was only logged.
func TestPartialAdapterFailureDegraded(t *testing.T) {
	e, dns, _, _ := persistEngine(t)
	dns.pointWarn = "whitelist not enforced on adapter(s): Wi-Fi"
	_, degraded, err := e.ApplyFilter("whitelist", nil, []string{"corp.example"})
	if err != nil || !strings.Contains(degraded, "Wi-Fi") {
		t.Errorf("degraded=%q err=%v, want the refused adapter named", degraded, err)
	}
}

func TestEmptyWhitelistDegraded(t *testing.T) {
	e, _, _, _ := persistEngine(t)
	count, degraded, err := e.ApplyFilter("whitelist", nil, nil)
	if err != nil || count != 0 || degraded == "" {
		t.Errorf("empty whitelist -> count=%d degraded=%q err=%v, want a degraded report", count, degraded, err)
	}
}

// expiringRecorder is a firewall whose entries lapse, like nftables here.
type expiringRecorder struct{ recordingFirewall }

func (*expiringRecorder) EntriesExpire() bool { return true }

// TestRefreshReappliesExpiringFirewall: Linux set elements expire after an hour
// and nothing put them back, while the device kept reporting the policy.
func TestRefreshReappliesExpiringFirewall(t *testing.T) {
	fw := &expiringRecorder{}
	e, _ := testEngine(t, fw)
	if _, _, err := e.ApplyBlockedDomains([]string{"detik.com"}); err != nil {
		t.Fatal(err)
	}
	before := len(fw.calls)
	if err := e.refreshOnce(); err != nil {
		t.Fatal(err)
	}
	if len(fw.calls) != before+1 || len(fw.last()) != 1 || !fw.last()[0].Equal(net.ParseIP("203.0.113.10")) {
		t.Errorf("firewall calls %d -> %d, last %v; want the blacklist re-installed", before, len(fw.calls), fw.last())
	}
}

func TestRewriteResolvConfDropsOtherNameservers(t *testing.T) {
	got := rewriteResolvConf("search corp.local\nnameserver 192.168.3.1\nnameserver 8.8.8.8\noptions edns0\n")
	want := "nameserver 127.0.0.1\nsearch corp.local\n# endpoint whitelist: nameserver 192.168.3.1\n# endpoint whitelist: nameserver 8.8.8.8\noptions edns0\n"
	if got != want {
		t.Errorf("rewrite =\n%s\nwant\n%s", got, want)
	}
	if up := upstreamFromBackup(dnsBackup{ResolvConf: "nameserver 127.0.0.53\nnameserver 192.168.3.1\n"}); up != "192.168.3.1:53" {
		t.Errorf("upstream from backup = %q", up)
	}
}

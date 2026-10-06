package networkfilter

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// fakeHub records what was sent and pretends the device is online.
type fakeHub struct {
	online   bool
	sent     [][]byte
	sendFail bool
}

func (f *fakeHub) Online(string) bool { return f.online }

func (f *fakeHub) SendTo(_ string, msg []byte) bool {
	if f.sendFail {
		return false
	}
	f.sent = append(f.sent, msg)
	return true
}

// connectFixture builds a handler wired to a throwaway database holding one
// device and one enabled device-scoped policy blocking one domain.
type connectFixture struct {
	h        *Handler
	repo     *Repository
	hub      *fakeHub
	database *sqlx.DB
	version  string
}

func newConnectFixture(t *testing.T) *connectFixture {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "reconnect.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	repo := NewRepository(database)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-1', 'PC-1', 'windows', '11.0', '1.0.0', 'Jakarta', 'online', ?, ?, 'h', ?, ?)
	`, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	p := &FilterPolicy{
		Name: "blacklist", TargetType: "device", TargetID: "dev-1",
		CreatedBy: "admin", IsEnabled: true, Priority: 100,
	}
	if err := repo.CreatePolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddRule(ctx, &FilterRule{
		PolicyID: p.ID, RuleType: "domain", Pattern: "detik.com", Action: "block", Category: "custom",
	}); err != nil {
		t.Fatal(err)
	}
	_, version, err := repo.CompileEffectiveRules(ctx, "dev-1")
	if err != nil {
		t.Fatal(err)
	}

	hub := &fakeHub{online: true}
	// nopAudit, not nil: syncDeviceFilter audits its dispatch, so a handler built
	// with a nil auditor panics on that path. Every test here used to reach only
	// SyncOnReconnect, which does not audit, so the nil survived.
	return &connectFixture{
		h:    NewHandler(repo, hub, devicemgmt.NewRepository(database), nopAudit{}, nil),
		repo: repo, hub: hub, database: database, version: version,
	}
}

// The defect this exists for: a manual sync clicked against a laptop that was
// asleep dispatched nothing, wrote status='pending', and returned. Nothing ever
// re-read that row, so the endpoint stayed unfiltered until an operator pressed
// Sync again. A device that has never reported a version must be sent the policy
// the moment it connects.
func TestReconnectDeliversToADeviceThatNeverSynced(t *testing.T) {
	f := newConnectFixture(t)
	ctx := context.Background()

	if err := f.h.SyncOnReconnect(ctx, "dev-1"); err != nil {
		t.Fatalf("sync on reconnect: %v", err)
	}
	if len(f.hub.sent) != 1 {
		t.Fatalf("%d messages sent, want 1: the device connected holding no policy at all", len(f.hub.sent))
	}
	assertFilterMessage(t, f.hub.sent[0], f.version, []string{"detik.com"})
}

// A device already enforcing the current version must be left alone. This is what
// keeps a reconnect storm from re-pushing an identical rule set to every endpoint.
func TestReconnectSkipsADeviceAlreadyCurrent(t *testing.T) {
	f := newConnectFixture(t)
	ctx := context.Background()

	if err := f.repo.RecordDeviceFilterState(ctx, &DeviceFilterState{
		DeviceID: "dev-1", PolicyVersion: f.version, Status: statusSynced, RulesApplied: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.h.SyncOnReconnect(ctx, "dev-1"); err != nil {
		t.Fatal(err)
	}
	if len(f.hub.sent) != 0 {
		t.Errorf("%d messages sent to an up-to-date device, want 0: every reconnect "+
			"would otherwise rewrite the same rules on the whole fleet", len(f.hub.sent))
	}
}

// 'failed' at the current version means the rules arrived and did not take effect,
// which is the case worth retrying.
func TestReconnectRetriesADeviceThatReportedFailure(t *testing.T) {
	f := newConnectFixture(t)
	ctx := context.Background()

	if err := f.repo.RecordDeviceFilterState(ctx, &DeviceFilterState{
		DeviceID: "dev-1", PolicyVersion: f.version, Status: statusFailed, RulesApplied: 0,
		ErrorMessage: "hosts file unwritable",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.h.SyncOnReconnect(ctx, "dev-1"); err != nil {
		t.Fatal(err)
	}
	if len(f.hub.sent) != 1 {
		t.Errorf("%d messages sent to a device stuck at 'failed', want 1", len(f.hub.sent))
	}
}

// 'degraded' means only the hosts layer is enforcing -- usually because the agent
// was not elevated. The rules have not changed, but re-running apply is the only
// thing that can fix it: a service restart that lands the agent under SYSTEM
// succeeds where the previous attempt was refused.
func TestReconnectRetriesADeviceRunningDegraded(t *testing.T) {
	f := newConnectFixture(t)
	ctx := context.Background()

	if err := f.repo.RecordDeviceFilterState(ctx, &DeviceFilterState{
		DeviceID: "dev-1", PolicyVersion: f.version, Status: "degraded", RulesApplied: 1,
		ErrorMessage: "firewall change refused",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.h.SyncOnReconnect(ctx, "dev-1"); err != nil {
		t.Fatal(err)
	}
	if len(f.hub.sent) != 1 {
		t.Fatalf("%d messages sent to a device enforcing only via the hosts file, want 1: "+
			"re-applying is what an elevation fix would rely on", len(f.hub.sent))
	}
}

// A device holding an older version is the ordinary case after a policy change.
func TestReconnectDeliversWhenThePolicyChanged(t *testing.T) {
	f := newConnectFixture(t)
	ctx := context.Background()

	if err := f.repo.RecordDeviceFilterState(ctx, &DeviceFilterState{
		DeviceID: "dev-1", PolicyVersion: "stale0000deadbeef", Status: statusSynced, RulesApplied: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.h.SyncOnReconnect(ctx, "dev-1"); err != nil {
		t.Fatal(err)
	}
	if len(f.hub.sent) != 1 {
		t.Fatalf("%d messages sent, want 1", len(f.hub.sent))
	}
	assertFilterMessage(t, f.hub.sent[0], f.version, []string{"detik.com"})
}

// An offline device has no socket to write to. Reporting that as a failure would
// log a warning on every reconnect of every sleeping laptop.
func TestReconnectSkipsAnOfflineDeviceQuietly(t *testing.T) {
	f := newConnectFixture(t)
	f.hub.online = false

	if err := f.h.SyncOnReconnect(context.Background(), "dev-1"); err != nil {
		t.Errorf("an offline device reported an error: %v", err)
	}
	if len(f.hub.sent) != 0 {
		t.Errorf("%d messages sent to an offline device", len(f.hub.sent))
	}
}

// A dropped connection must surface as an error the caller can log, not as a
// silent success -- otherwise the operator's policy change simply vanishes.
func TestReconnectReportsAFailedSend(t *testing.T) {
	f := newConnectFixture(t)
	f.hub.sendFail = true

	if err := f.h.SyncOnReconnect(context.Background(), "dev-1"); err == nil {
		t.Error("a send that never reached the device was reported as delivered")
	}
}

// The manual sync and the reconnect hook must send byte-identical shapes. An agent
// that reads one and not the other would end up enforcing a policy the console
// never shows.
func assertFilterMessage(t *testing.T, raw []byte, wantVersion string, wantDomains []string) {
	t.Helper()
	var env struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Command string `json:"command"`
		Payload struct {
			PolicyVersion  string   `json:"policy_version"`
			BlockedDomains []string `json:"blocked_domains"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Command != "filter.apply" {
		t.Errorf("command = %q, want filter.apply", env.Command)
	}
	if env.Payload.PolicyVersion != wantVersion {
		t.Errorf("payload version = %q, want %q", env.Payload.PolicyVersion, wantVersion)
	}
	if len(env.Payload.BlockedDomains) != len(wantDomains) {
		t.Fatalf("blocked domains = %v, want %v", env.Payload.BlockedDomains, wantDomains)
	}
	for i, d := range wantDomains {
		if env.Payload.BlockedDomains[i] != d {
			t.Errorf("blocked domain %d = %q, want %q", i, env.Payload.BlockedDomains[i], d)
		}
	}
}

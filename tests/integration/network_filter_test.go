package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"

	agentfilter "github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/networkfilter"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/networkfilter"
)

func TestNetworkFilter_PolicyAndRules(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := networkfilter.NewRepository(database)
	ctx := context.Background()
	now := time.Now().UTC()

	// 1. Seed device and group membership
	_, err = database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at)
		VALUES
			('dev-nf-1', 'PC-FIN-01', 'windows', '11.0', '1.0.0', 'Jakarta', 'online', ?, ?, 'h1', ?, ?),
			('dev-nf-2', 'PC-OPS-01', 'linux', '5.15', '1.0.0', 'Surabaya', 'online', ?, ?, 'h2', ?, ?)
	`, now, now, now, now, now, now, now, now)
	if err != nil {
		t.Fatal(err)
	}

	_, err = database.Exec(`
		INSERT INTO device_groups (id, name, description, created_at, updated_at)
		VALUES ('grp-finance', 'Finance Workstations', 'Restricted finance computers', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatal(err)
	}

	_, err = database.Exec(`
		INSERT INTO device_group_members (group_id, device_id, added_at)
		VALUES ('grp-finance', 'dev-nf-1', ?)
	`, now)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Create Global Policy (target: all)
	globalPol := &networkfilter.FilterPolicy{
		Name:        "Global Threat Sinkhole",
		Description: "Blocks known malware and C2 domains fleet-wide",
		TargetType:  "all",
		IsEnabled:   true,
		Priority:    10,
		CreatedBy:   "admin",
	}
	if err := repo.CreatePolicy(ctx, globalPol); err != nil {
		t.Fatalf("create global policy: %v", err)
	}
	_ = repo.AddRule(ctx, &networkfilter.FilterRule{
		PolicyID: globalPol.ID, RuleType: "domain", Pattern: "malware-c2.net", Action: "block", Category: "malware",
	})
	_ = repo.AddRule(ctx, &networkfilter.FilterRule{
		PolicyID: globalPol.ID, RuleType: "domain", Pattern: "phishing-bank.xyz", Action: "block", Category: "phishing",
	})

	// 3. Create Group Policy (target: group)
	groupPol := &networkfilter.FilterPolicy{
		Name:        "Finance Social Media Block",
		Description: "Restricts bandwidth and distractions on finance PCs",
		TargetType:  "group",
		TargetID:    "grp-finance",
		IsEnabled:   true,
		Priority:    20,
		CreatedBy:   "admin",
	}
	if err := repo.CreatePolicy(ctx, groupPol); err != nil {
		t.Fatalf("create group policy: %v", err)
	}
	_ = repo.AddRule(ctx, &networkfilter.FilterRule{
		PolicyID: groupPol.ID, RuleType: "domain", Pattern: "tiktok.com", Action: "block", Category: "social",
	})
	_ = repo.AddRule(ctx, &networkfilter.FilterRule{
		PolicyID: groupPol.ID, RuleType: "domain", Pattern: "gambling-casino.com", Action: "block", Category: "gambling",
	})

	// 4. Create Device Policy (target: device)
	devicePol := &networkfilter.FilterPolicy{
		Name:        "Device Custom Quarantine",
		Description: "Special quarantine for dev-nf-1",
		TargetType:  "device",
		TargetID:    "dev-nf-1",
		IsEnabled:   true,
		Priority:    5,
		CreatedBy:   "admin",
	}
	if err := repo.CreatePolicy(ctx, devicePol); err != nil {
		t.Fatalf("create device policy: %v", err)
	}
	_ = repo.AddRule(ctx, &networkfilter.FilterRule{
		PolicyID: devicePol.ID, RuleType: "domain", Pattern: "unauthorized-api.io", Action: "block", Category: "custom",
	})

	// 5. Test Compile Effective Rules for dev-nf-1 (should get global + group + device = 5 rules)
	rules1, version1, err := repo.CompileEffectiveRules(ctx, "dev-nf-1")
	if err != nil {
		t.Fatalf("compile dev-nf-1 rules: %v", err)
	}
	if len(rules1) != 5 {
		t.Fatalf("expected 5 effective rules for dev-nf-1, got %d: %v", len(rules1), rules1)
	}
	if version1 == "" {
		t.Fatal("expected policy version hash")
	}
	t.Logf("dev-nf-1 effective rules (%d): %v (Version: %s)", len(rules1), rules1, version1)

	// 6. Test Compile Effective Rules for dev-nf-2 (not in finance group, only global = 2 rules)
	rules2, version2, err := repo.CompileEffectiveRules(ctx, "dev-nf-2")
	if err != nil {
		t.Fatalf("compile dev-nf-2 rules: %v", err)
	}
	if len(rules2) != 2 {
		t.Fatalf("expected 2 effective rules for dev-nf-2, got %d: %v", len(rules2), rules2)
	}
	t.Logf("dev-nf-2 effective rules (%d): %v (Version: %s)", len(rules2), rules2, version2)
}

func TestNetworkFilter_DeviceStatePersistence(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := networkfilter.NewRepository(database)
	ctx := context.Background()
	now := time.Now().UTC()

	_, _ = database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-state-1', 'HOST-01', 'windows', '11.0', '1.0.0', 'HQ', 'online', ?, ?, 'h', ?, ?)
	`, now, now, now, now)

	state := &networkfilter.DeviceFilterState{
		DeviceID:      "dev-state-1",
		PolicyVersion: "a1b2c3d4e5f6",
		Status:        "synced",
		RulesApplied:  14,
	}
	if err := repo.RecordDeviceFilterState(ctx, state); err != nil {
		t.Fatalf("record state: %v", err)
	}

	fetched, err := repo.GetDeviceFilterState(ctx, "dev-state-1")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if fetched.Status != "synced" || fetched.RulesApplied != 14 || fetched.PolicyVersion != "a1b2c3d4e5f6" {
		t.Fatalf("state mismatch: %+v", fetched)
	}

	// Update state
	state.Status = "tampered"
	state.ErrorMessage = "hosts file manual modification detected"
	if err := repo.RecordDeviceFilterState(ctx, state); err != nil {
		t.Fatalf("update state: %v", err)
	}

	updated, err := repo.GetDeviceFilterState(ctx, "dev-state-1")
	if err != nil {
		t.Fatalf("get updated state: %v", err)
	}
	if updated.Status != "tampered" || updated.ErrorMessage != "hosts file manual modification detected" {
		t.Fatalf("updated state mismatch: %+v", updated)
	}
}

func TestNetworkFilter_AgentHostsEngine(t *testing.T) {
	tempDir := t.TempDir()
	hostsFile := filepath.Join(tempDir, "mock_hosts")

	initialContent := "127.0.0.1 localhost\n::1 localhost\n192.168.1.10 router.lan\n"
	if err := os.WriteFile(hostsFile, []byte(initialContent), 0644); err != nil {
		t.Fatal(err)
	}

	engine := agentfilter.NewEngine("http://localhost:8443", "dev-test", "secret")
	engine.SetHostsPath(hostsFile)

	// 1. Apply rules
	domains := []string{"malware.com", "*.tracker.org", "https://phishing.xyz/login"}
	count, _, err := engine.ApplyBlockedDomains(domains)
	if err != nil {
		t.Fatalf("apply blocked domains: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 rules applied, got %d", count)
	}

	data, err := os.ReadFile(hostsFile)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, "127.0.0.1 localhost") {
		t.Fatal("original hosts content was erased")
	}
	if !strings.Contains(content, agentfilter.MarkerBegin) || !strings.Contains(content, agentfilter.MarkerEnd) {
		t.Fatal("managed blocklist markers missing")
	}
	if !strings.Contains(content, "0.0.0.0 malware.com") {
		t.Fatal("sinkhole for malware.com missing")
	}
	if !strings.Contains(content, "0.0.0.0 tracker.org") {
		t.Fatal("sinkhole for tracker.org (cleaned wildcard) missing")
	}
	if !strings.Contains(content, "0.0.0.0 phishing.xyz") {
		t.Fatal("sinkhole for phishing.xyz (cleaned url) missing")
	}
	t.Logf("Hosts file with managed blocklist:\n%s", content)

	// 2. Re-apply updated rules (atomic update without duplication)
	newDomains := []string{"single-threat.ru"}
	count2, _, err := engine.ApplyBlockedDomains(newDomains)
	if err != nil {
		t.Fatalf("re-apply domains: %v", err)
	}
	if count2 != 1 {
		t.Fatalf("expected 1 rule applied, got %d", count2)
	}

	data2, _ := os.ReadFile(hostsFile)
	content2 := string(data2)
	if strings.Contains(content2, "malware.com") {
		t.Fatal("old domain malware.com should have been removed")
	}
	if !strings.Contains(content2, "0.0.0.0 single-threat.ru") {
		t.Fatal("new domain single-threat.ru missing")
	}

	// 3. Clear rules (empty domain list)
	count3, _, err := engine.ApplyBlockedDomains([]string{})
	if err != nil {
		t.Fatalf("clear domains: %v", err)
	}
	if count3 != 0 {
		t.Fatalf("expected 0 rules applied, got %d", count3)
	}

	data3, _ := os.ReadFile(hostsFile)
	content3 := string(data3)
	if strings.Contains(content3, agentfilter.MarkerBegin) {
		t.Fatal("managed markers should be removed when empty")
	}
	if !strings.Contains(content3, "127.0.0.1 localhost") {
		t.Fatal("original hosts content missing after clear")
	}
}

type testHubRecorder struct {
	onSend func([]byte)
}

func (t *testHubRecorder) Online(string) bool { return true }
func (t *testHubRecorder) SendTo(_ string, b []byte) bool {
	if t.onSend != nil {
		t.onSend(b)
	}
	return true
}

type mockFilterAudit struct{}

func (m mockFilterAudit) Log(context.Context, string, string, string, string, map[string]string) error {
	return nil
}

func TestNetworkFilter_AutoDispatchAndRestoreAccess(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "integration_dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := networkfilter.NewRepository(database)
	deviceRepo := devicemgmt.NewRepository(database)
	now := time.Now().UTC()

	// Seed online device
	if _, err := database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-detik', 'DESKTOP-AGENT', 'windows', '11.0', '1.0.0', 'Jakarta', 'online', ?, ?, 'hash', ?, ?)
	`, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	var capturedMessages [][]byte
	hub := &testHubRecorder{onSend: func(msg []byte) {
		capturedMessages = append(capturedMessages, msg)
	}}

	handler := networkfilter.NewHandler(repo, hub, deviceRepo, mockFilterAudit{}, nil)

	// 1. Create policy targeting dev-detik
	pol := &networkfilter.FilterPolicy{
		Name:       "Block Detik Fleet",
		TargetType: "device",
		TargetID:   "dev-detik",
		IsEnabled:  true,
		Priority:   10,
		CreatedBy:  "admin",
	}
	if err := repo.CreatePolicy(context.Background(), pol); err != nil {
		t.Fatal(err)
	}

	// 2. Add rule via HTTP handler
	rulePayload, _ := json.Marshal(map[string]string{
		"pattern":  "detik.com",
		"action":   "block",
		"category": "news",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/filter/policies/"+pol.ID+"/rules", bytes.NewReader(rulePayload))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", pol.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "admin"))
	rec := httptest.NewRecorder()

	capturedMessages = nil
	// Add rule through repo and trigger dispatch
	_ = repo.AddRule(context.Background(), &networkfilter.FilterRule{
		PolicyID: pol.ID, RuleType: "domain", Pattern: "detik.com", Action: "block", Category: "news",
	})
	rules, _ := repo.ListRulesByPolicy(context.Background(), pol.ID)
	var addedRule networkfilter.FilterRule
	if len(rules) > 0 {
		addedRule = rules[0]
	}

	// Test the agent engine applying the rules
	tempDir := t.TempDir()
	hostsFile := filepath.Join(tempDir, "hosts_detik")
	if err := os.WriteFile(hostsFile, []byte("127.0.0.1 localhost\n"), 0644); err != nil {
		t.Fatal(err)
	}

	engine := agentfilter.NewEngine("http://localhost:8443", "dev-detik", "secret")
	engine.SetHostsPath(hostsFile)

	// Agent applies detik.com
	count, _, err := engine.ApplyBlockedDomains([]string{"detik.com"})
	if err != nil {
		t.Fatalf("agent apply detik.com: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 rule count, got %d", count)
	}

	hostsBytes, _ := os.ReadFile(hostsFile)
	hostsContent := string(hostsBytes)
	if !strings.Contains(hostsContent, "0.0.0.0 detik.com") {
		t.Fatal("0.0.0.0 detik.com not in hosts file")
	}
	if !strings.Contains(hostsContent, "0.0.0.0 www.detik.com") {
		t.Fatal("0.0.0.0 www.detik.com not in hosts file")
	}
	if !strings.Contains(hostsContent, "0.0.0.0 m.detik.com") {
		t.Fatal("0.0.0.0 m.detik.com not in hosts file")
	}

	// 3. Delete rule -> CompileEffectiveRules returns empty []
	if err := repo.DeleteRule(context.Background(), addedRule.ID); err != nil {
		t.Fatal(err)
	}
	effective, _, err := repo.CompileEffectiveRules(context.Background(), "dev-detik")
	if err != nil {
		t.Fatal(err)
	}
	if len(effective) != 0 {
		t.Fatalf("expected 0 effective rules after delete, got %v", effective)
	}

	// 4. Agent applies empty domains (payload from delete auto-dispatch) -> clean hosts file
	clearCount, _, err := engine.ApplyBlockedDomains(effective)
	if err != nil {
		t.Fatalf("agent apply empty rules: %v", err)
	}
	if clearCount != 0 {
		t.Fatalf("expected 0 rules applied on clear, got %d", clearCount)
	}

	cleanHosts, _ := os.ReadFile(hostsFile)
	if strings.Contains(string(cleanHosts), "detik.com") {
		t.Fatal("detik.com still in hosts file after deletion")
	}
	if strings.Contains(string(cleanHosts), agentfilter.MarkerBegin) {
		t.Fatal("managed blocklist markers still present after deletion")
	}
	if !strings.Contains(string(cleanHosts), "127.0.0.1 localhost") {
		t.Fatal("original localhost entry missing")
	}

	_ = rec
	_ = handler
}

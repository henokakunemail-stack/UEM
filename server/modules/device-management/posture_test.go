package devicemanagement

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

func boolp(b bool) *bool { return &b }

func fullPosture() protocol.Posture {
	return protocol.Posture{
		SchemaVersion: 1,
		DiskEncryption: protocol.DiskEncryptionPosture{State: "reported", Volumes: []protocol.EncryptedVolume{
			{Mount: "C:", System: true, Protected: true}, {Mount: "D:", Protected: false}}},
		Antivirus: protocol.AntivirusPosture{State: "reported", Products: []protocol.AVProduct{
			{Name: "Old AV", Enabled: boolp(false)}, {Name: "Defender", Enabled: boolp(true)}}},
		Firewall: protocol.FirewallPosture{State: "reported", Profiles: []protocol.FirewallProfile{
			{Name: "Domain", Enabled: true}, {Name: "Private", Enabled: true}, {Name: "Public", Enabled: true}}},
		PasswordPolicy: protocol.PasswordPolicyPosture{State: "reported"},
		ScreenLock:     protocol.ScreenLockPosture{State: "not_applicable"},
	}
}

func TestSummarizePosture(t *testing.T) {
	got := summarizePosture(fullPosture())
	want := postureSummary{DiskEncryption: "partial", Antivirus: "on", Firewall: "on",
		PasswordPolicy: "reported", ScreenLock: "not_applicable"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	p := fullPosture()
	p.Antivirus.Products = nil
	p.Firewall.Profiles[2].Enabled = false
	p.DiskEncryption.Volumes = nil
	got = summarizePosture(p)
	if got.Antivirus != "off" || got.Firewall != "partial" || got.DiskEncryption != "unknown" {
		t.Fatalf("no AV must be off, one profile down partial, no volumes unknown; got %+v", got)
	}
}

// A state outside the contract is device input the server does not
// understand; it must not read as a pass.
func TestSummarizePostureUnrecognisedStateIsUnknown(t *testing.T) {
	p := fullPosture()
	p.Firewall.State = "compliant"
	p.Antivirus.State = ""
	got := summarizePosture(p)
	if got.Firewall != "unknown" || got.Antivirus != "unknown" {
		t.Fatalf("got %+v", got)
	}
}

func acceptWithPosture(t *testing.T, h *inventoryHandler, p *protocol.Posture) {
	t.Helper()
	rep := map[string]any{
		"hardware": json.RawMessage(`{"cpu":{"name":"X"}}`), "software": json.RawMessage(`[]`),
		"os": json.RawMessage(`{"edition":"Pro"}`), "collected_at": "2026-11-19T01:00:00Z",
	}
	if p != nil {
		rep["posture"] = p
	}
	raw, _ := json.Marshal(rep)
	if err := h.acceptInventory(t.Context(), "dev-1", raw); err != nil {
		t.Fatalf("accept: %v", err)
	}
}

func getPostureResp(t *testing.T, h *inventoryHandler) postureResponse {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/devices/{id}/posture", h.getPosture)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/devices/dev-1/posture", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp postureResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// An agent that predates posture must read as "not reported", not compliant.
func TestOldAgentInventoryLeavesPostureUnreported(t *testing.T) {
	db := newTestDB(t)
	seedDevice(t, db, "dev-1")
	h := newInventoryHandler(newInventoryRepository(db), &recordingAudit{}, offlineHub{})
	acceptWithPosture(t, h, nil)
	resp := getPostureResp(t, h)
	if resp.Reported || resp.Summary != nil || resp.Reason == "" {
		t.Fatalf("old agent: %+v", resp)
	}
}

func TestPostureStoredWithHistoryOnlyOnChange(t *testing.T) {
	db := newTestDB(t)
	seedDevice(t, db, "dev-1")
	h := newInventoryHandler(newInventoryRepository(db), &recordingAudit{}, offlineHub{})

	p := fullPosture()
	acceptWithPosture(t, h, &p)
	acceptWithPosture(t, h, &p) // unchanged: no new history row
	p.Firewall.Profiles[2].Enabled = false
	acceptWithPosture(t, h, &p)

	resp := getPostureResp(t, h)
	if !resp.Reported || resp.Summary == nil || resp.Summary.Firewall != "partial" {
		t.Fatalf("latest: %+v", resp)
	}
	if len(resp.History) != 2 {
		t.Fatalf("history rows = %d, want 2 (first report + firewall change)", len(resp.History))
	}
	var back protocol.Posture
	if err := json.Unmarshal(resp.Posture, &back); err != nil || len(back.Firewall.Profiles) != 3 {
		t.Fatalf("detail not round-tripped: %v %+v", err, back.Firewall)
	}
}

// A malformed posture section must not cost the inventory it came with.
func TestBadPostureDoesNotLoseInventory(t *testing.T) {
	db := newTestDB(t)
	seedDevice(t, db, "dev-1")
	repo := newInventoryRepository(db)
	h := newInventoryHandler(repo, &recordingAudit{}, offlineHub{})
	raw := []byte(`{"hardware":{"cpu":{"name":"X"}},"software":[],"os":{},"posture":"not-an-object"}`)
	if err := h.acceptInventory(t.Context(), "dev-1", raw); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := repo.getInventory(t.Context(), "dev-1"); err != nil {
		t.Fatalf("inventory lost: %v", err)
	}
	if resp := getPostureResp(t, h); resp.Reported {
		t.Fatalf("garbage posture stored: %+v", resp)
	}
}

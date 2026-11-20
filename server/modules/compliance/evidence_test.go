package compliance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func evidenceCtx(t *testing.T, g *Generator) *EvidenceContext {
	t.Helper()
	ctx := context.Background()
	p, err := g.Repo.GetProfile(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	ec := &EvidenceContext{Gen: g, Now: time.Now().UTC(), Profile: p}
	ec.Results, _ = g.Repo.Evaluate(ctx, p, ec.Now)
	ec.Device = Summarize(p.Rules, ec.Results)
	return ec
}

func collect(t *testing.T, ec *EvidenceContext, source string, params map[string]any) EvidenceResult {
	t.Helper()
	return ec.Collect(context.Background(), EvidenceRef{Source: source, Params: params})
}

func TestDeviceEvidence(t *testing.T) {
	d := newTestDB(t)
	seedFleet(t, d)
	ec := evidenceCtx(t, newGen(t, d))
	disk := collect(t, ec, "device.disk_encryption", nil)
	if disk.Status != EvidencePartial || !strings.Contains(disk.Summary, "1 dari 2") ||
		!strings.Contains(disk.Summary, "1 perangkat tidak dapat dinilai") || len(disk.Details) != 1 {
		t.Fatalf("disk evidence %+v", disk)
	}
	// 2 of 2 judged pass, but one device is unknown: partial, not supported.
	if av := collect(t, ec, "device.antivirus", nil); av.Status != EvidencePartial {
		t.Fatalf("antivirus %+v", av)
	}
	// Patches were judged on all three devices.
	if p := collect(t, ec, "device.patches", nil); p.Status != EvidenceSupported {
		t.Fatalf("patches %+v", p)
	}
	ec.Profile.Rules.RequireScreenLock = false
	ec.Device = Summarize(ec.Profile.Rules, ec.Results)
	if sl := collect(t, ec, "device.screen_lock", nil); sl.Status != EvidenceNoData {
		t.Fatalf("a control the profile does not judge has no data: %+v", sl)
	}
}

func TestBackupEvidence(t *testing.T) {
	g := newGen(t, newTestDB(t))
	g.BackupDir = t.TempDir()
	g.BackupInterval = time.Hour
	ec := evidenceCtx(t, g)
	if r := collect(t, ec, "backup.recent", nil); r.Status != EvidenceGap {
		t.Fatalf("no backups: %+v", r)
	}
	f := filepath.Join(g.BackupDir, "auto-1.db")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	if r := collect(t, ec, "backup.recent", nil); r.Status != EvidenceSupported {
		t.Fatalf("fresh backup: %+v", r)
	}
	old := time.Now().Add(-30 * time.Hour)
	_ = os.Chtimes(f, old, old)
	if r := collect(t, ec, "backup.recent", nil); r.Status != EvidenceGap {
		t.Fatalf("30h-old backup with a 2h limit: %+v", r)
	}
	// A template may ask for a looser or stricter window.
	if r := collect(t, ec, "backup.recent", map[string]any{"max_age_hours": float64(48)}); r.Status != EvidenceSupported {
		t.Fatalf("30h-old backup with a 48h limit: %+v", r)
	}
	g.DBDriver = "postgres-rebind"
	if r := collect(t, ec, "backup.recent", nil); r.Status != EvidenceNoData {
		t.Fatalf("postgres: %+v", r)
	}
}

// A USB block counts only where the policy asks for it and the agent reported
// that setting applied.
func TestUSBPolicyEvidence(t *testing.T) {
	d := newTestDB(t)
	seedFleet(t, d)
	g := newGen(t, d)
	now := time.Now().UTC()
	for _, s := range []struct{ dev, applied, results string }{
		{"PC-OK", `{"usb_storage_mode":"block"}`, `[{"setting":"usb_storage_mode","outcome":"applied"}]`},
		{"PC-BITLOCKER-OFF", `{"usb_storage_mode":"block"}`, `[{"setting":"usb_storage_mode","outcome":"failed","reason":"access denied"}]`},
		{"PC-OLD-AGENT", `{"usb_storage_mode":"allow"}`, `[{"setting":"usb_storage_mode","outcome":"applied"}]`},
	} {
		if _, err := d.Exec(`INSERT INTO device_system_policy_states (device_id, policy_version, status, applied_settings_json,
			results_json, last_applied_at) VALUES (?, 'v1', 'synced', ?, ?, ?)`, s.dev, s.applied, s.results, now); err != nil {
			t.Fatal(err)
		}
	}
	r := collect(t, evidenceCtx(t, g), "policy.usb_block", nil)
	if r.Status != EvidencePartial || !strings.Contains(r.Summary, "1 dari 3") ||
		len(r.Details) != 1 || !strings.Contains(r.Details[0], "access denied") {
		t.Fatalf("usb evidence %+v", r)
	}
}

func TestOtherEvidence(t *testing.T) {
	d := newTestDB(t)
	seedFleet(t, d)
	g := newGen(t, d)
	ec := evidenceCtx(t, g)
	if r := collect(t, ec, "network.web_filter", nil); r.Status != EvidenceGap {
		t.Fatalf("no filter policy: %+v", r)
	}
	if r := collect(t, ec, "licenses.tracked", nil); r.Status != EvidenceNoData {
		t.Fatalf("no licenses: %+v", r)
	}
	if r := collect(t, ec, "assets.register", nil); r.Status != EvidenceGap {
		t.Fatalf("no assets: %+v", r)
	}
	if r := collect(t, ec, "inventory.coverage", nil); r.Status != EvidenceGap || !strings.Contains(r.Summary, "0 dari 3") {
		t.Fatalf("no inventory: %+v", r)
	}
	// newGen's EnsureDefaultProfile writes nothing to the audit log, so seed one entry.
	_ = g.Audit.Log(context.Background(), "system", "", "test.entry", "", nil)
	if r := collect(t, ec, "audit.chain", nil); r.Status != EvidenceSupported {
		t.Fatalf("audit chain: %+v", r)
	}
	if r := collect(t, ec, "access.rbac", nil); r.Status != EvidenceNoData {
		t.Fatalf("no users: %+v", r)
	}
	if r := collect(t, ec, "server.security_audit", nil); r.Status != EvidenceNoData {
		t.Fatalf("no audit function: %+v", r)
	}
	g.ServerAudit = func(context.Context) (*ServerAuditResult, error) {
		return &ServerAuditResult{Grade: "B", Score: 80, Passed: 30, Failed: 2, FailedTitles: []string{"TLS off"}}, nil
	}
	if r := collect(t, ec, "server.security_audit", nil); r.Status != EvidencePartial || r.Details[0] != "TLS off" {
		t.Fatalf("server audit: %+v", r)
	}
	if r := collect(t, ec, "no.such", nil); r.Status != EvidenceNoData {
		t.Fatalf("unknown source: %+v", r)
	}
}

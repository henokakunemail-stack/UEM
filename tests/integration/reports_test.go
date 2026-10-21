package integration

import (
	"context"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/reports"
)

func TestReports_Aggregations(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := reports.NewRepository(database)
	ctx := context.Background()

	now := time.Now().UTC()

	// 1. Seed device & inventory
	_, err = database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-rep-1', 'PC-SURABAYA-01', 'windows', '11.0', '1.0.0', 'Surabaya', 'online', ?, ?, 'hash', ?, ?)
	`, now, now, now, now)
	if err != nil {
		t.Fatal("seed device:", err)
	}

	_, err = database.Exec(`
		INSERT INTO device_inventory (id, device_id, hw, software, os_detail, hw_ram_bytes, hw_disk_free_pct, hw_cpu_model, collected_at, updated_at)
		VALUES ('inv-1', 'dev-rep-1', '{}', '[]', '{}', 17179869184, 45.5, 'Intel i5-1135G7', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatal("seed inventory:", err)
	}

	// Test Inventory Report
	invRows, _, err := repo.GetDeviceInventoryReport(ctx, "", "")
	if err != nil {
		t.Fatal("get inventory report:", err)
	}
	if len(invRows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(invRows))
	}
	if invRows[0].Hostname != "PC-SURABAYA-01" || *invRows[0].RAMBytes != 17179869184 {
		t.Fatalf("unexpected inventory row: %+v", invRows[0])
	}
	t.Logf("Inventory report verified: %s (%s, %.1f%% disk free)",
		invRows[0].Hostname, *invRows[0].CPUModel, *invRows[0].DiskFreePct)

	// 2. Seed patch
	_, err = database.Exec(`
		INSERT INTO device_patches (id, device_id, patch_id, title, severity, category, installed_state, discovered_at, updated_at)
		VALUES ('p-1', 'dev-rep-1', 'KB5034441', 'Security Fix', 'critical', 'security', 'missing', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatal("seed patch:", err)
	}

	patchRows, _, err := repo.GetPatchComplianceReport(ctx, "", "")
	if err != nil {
		t.Fatal("get patch report:", err)
	}
	if len(patchRows) != 1 {
		t.Fatalf("expected 1 patch row, got %d", len(patchRows))
	}
	if patchRows[0].Severity != "critical" {
		t.Fatalf("expected critical, got %s", patchRows[0].Severity)
	}
	t.Logf("Patch compliance report verified: %s (%s, %s)",
		patchRows[0].PatchID, patchRows[0].Title, patchRows[0].Severity)

	// 3. Seed audit log
	_, err = database.Exec(`
		INSERT INTO audit_logs (id, actor_type, actor_id, action, target_id, details, created_at)
		VALUES ('a-1', 'user', 'admin-id', 'device.enroll', 'dev-rep-1', '{"site":"Surabaya"}', ?)
	`, now)
	if err != nil {
		t.Fatal("seed audit log:", err)
	}

	auditRows, _, err := repo.GetAuditTrailReport(ctx, "", 10)
	if err != nil {
		t.Fatal("get audit report:", err)
	}
	if len(auditRows) < 1 {
		t.Fatal("expected at least 1 audit row")
	}
	t.Logf("Audit trail report verified: %s by %s", auditRows[0].Action, auditRows[0].ActorID)
}

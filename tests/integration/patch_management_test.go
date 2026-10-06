package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
	patchmgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/patch-management"
)

func TestPatchManagement_RBAC(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := patchmgmt.NewRepository(database)
	ctx := context.Background()

	// Create test device
	createTestDeviceForPatch(t, database, "dev-patch-1", "PATCH-WINDOWS-1")

	// Upsert patches
	patches := []patchmgmt.DevicePatch{
		{
			PatchID:        "KB5034441",
			Title:          "Windows Security Update KB5034441",
			Severity:       patchmgmt.SeverityCritical,
			Category:       patchmgmt.CategorySecurity,
			KBID:           "KB5034441",
			InstalledState: patchmgmt.StateMissing,
		},
		{
			PatchID:        "KB5034123",
			Title:          "Windows Cumulative Update KB5034123",
			Severity:       patchmgmt.SeverityImportant,
			Category:       patchmgmt.CategoryUpdates,
			KBID:           "KB5034123",
			InstalledState: patchmgmt.StateMissing,
		},
	}
	if err := repo.UpsertPatches(ctx, "dev-patch-1", patches); err != nil {
		t.Fatal("upsert patches:", err)
	}

	// List device patches
	listed, err := repo.ListDevicePatches(ctx, "dev-patch-1", "")
	if err != nil {
		t.Fatal("list patches:", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 patches, got %d", len(listed))
	}
	t.Logf("Listed %d patches for dev-patch-1", len(listed))

	// List missing only
	missing, err := repo.ListDevicePatches(ctx, "dev-patch-1", "missing")
	if err != nil {
		t.Fatal("list missing patches:", err)
	}
	if len(missing) != 2 {
		t.Fatalf("expected 2 missing patches, got %d", len(missing))
	}

	// Fleet summary
	summary, err := repo.GetFleetSummary(ctx)
	if err != nil {
		t.Fatal("fleet summary:", err)
	}
	if summary.TotalMissingPatches != 2 {
		t.Fatalf("expected 2 total missing, got %d", summary.TotalMissingPatches)
	}
	if summary.CriticalSecurityPatches < 1 {
		t.Fatalf("expected at least 1 critical/security, got %d", summary.CriticalSecurityPatches)
	}
	t.Logf("Fleet summary: missing=%d, critical=%d, vulnerable=%d",
		summary.TotalMissingPatches, summary.CriticalSecurityPatches, summary.VulnerableDevices)
}

func TestPatchManagement_InstallJobLifecycle(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := patchmgmt.NewRepository(database)
	ctx := context.Background()

	// Create test device and user
	createTestDeviceForPatch(t, database, "dev-patch-2", "PATCH-LINUX-1")
	createTestUserForPatch(t, database, "tech-patch-1", "patchtechuser", "technician")

	// Upsert a patch
	patches := []patchmgmt.DevicePatch{
		{
			PatchID:        "curl-7.88.1",
			Title:          "Update for curl-7.88.1",
			Severity:       patchmgmt.SeverityImportant,
			Category:       patchmgmt.CategorySecurity,
			InstalledState: patchmgmt.StateMissing,
		},
	}
	if err := repo.UpsertPatches(ctx, "dev-patch-2", patches); err != nil {
		t.Fatal("upsert patches:", err)
	}

	// Create install job
	patchIDsJSON, _ := json.Marshal([]string{"curl-7.88.1"})
	job := &patchmgmt.PatchInstallJob{
		ID:           "job-patch-1",
		DeviceID:     "dev-patch-2",
		OperatorID:   "tech-patch-1",
		PatchIDs:     string(patchIDsJSON),
		Status:       patchmgmt.JobStatusDispatched,
		RebootPolicy: patchmgmt.RebootPolicyNoReboot,
		StartedAt:    time.Now().UTC(),
	}
	if err := repo.CreateInstallJob(ctx, job); err != nil {
		t.Fatal("create install job:", err)
	}

	// Get job
	fetched, err := repo.GetInstallJob(ctx, "job-patch-1")
	if err != nil {
		t.Fatal("get install job:", err)
	}
	if fetched.Status != patchmgmt.JobStatusDispatched {
		t.Fatalf("expected status dispatched, got %s", fetched.Status)
	}

	// Report install result.
	//
	// The device id is an argument, not something read out of the report. The
	// UPDATE used to match on the job id alone, so any agent could complete or
	// fail any other device's install job and have the fleet's patch state
	// recomputed from a result it made up. The unit test that covers this is in
	// server/modules/patch-management/report_authz_test.go; this line is here so
	// the signature change is exercised by the integration suite too.
	report := patchmgmt.PatchInstallReport{
		JobID:          "job-patch-1",
		Status:         patchmgmt.JobStatusCompleted,
		RebootRequired: false,
		OutputLog:      "Successfully installed curl-7.88.1",
	}
	if err := repo.UpdateInstallJobResult(ctx, job.DeviceID, report); err != nil {
		t.Fatal("update install job result:", err)
	}

	// The same report from a different device must not land, or the scoping is
	// cosmetic.
	if err := repo.UpdateInstallJobResult(ctx, "dev-patch-9", patchmgmt.PatchInstallReport{
		JobID:     "job-patch-1",
		Status:    patchmgmt.JobStatusFailed,
		OutputLog: "owned by someone else",
	}); !errors.Is(err, patchmgmt.ErrNotFound) {
		t.Errorf("another device's report returned %v, want ErrNotFound", err)
	}

	// Verify job updated
	updated, err := repo.GetInstallJob(ctx, "job-patch-1")
	if err != nil {
		t.Fatal("get updated job:", err)
	}
	if updated.Status != patchmgmt.JobStatusCompleted {
		t.Fatalf("expected status completed, got %s", updated.Status)
	}
	if updated.CompletedAt == nil {
		t.Fatal("expected completed_at to be set")
	}

	// Verify patch state was updated
	devPatches, err := repo.ListDevicePatches(ctx, "dev-patch-2", "installed")
	if err != nil {
		t.Fatal("list installed patches:", err)
	}
	if len(devPatches) != 1 {
		t.Fatalf("expected 1 installed patch after job completion, got %d", len(devPatches))
	}
	if devPatches[0].PatchID != "curl-7.88.1" {
		t.Fatalf("expected patch curl-7.88.1, got %s", devPatches[0].PatchID)
	}
	t.Logf("Install job lifecycle verified: %s -> %s", job.Status, updated.Status)
}

func TestPatchManagement_UpsertIdempotency(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := patchmgmt.NewRepository(database)
	ctx := context.Background()

	createTestDeviceForPatch(t, database, "dev-patch-3", "IDEM-TEST-1")

	patches := []patchmgmt.DevicePatch{
		{
			PatchID:        "KB9999999",
			Title:          "Test Patch v1",
			Severity:       patchmgmt.SeverityLow,
			Category:       patchmgmt.CategoryUpdates,
			InstalledState: patchmgmt.StateMissing,
		},
	}
	if err := repo.UpsertPatches(ctx, "dev-patch-3", patches); err != nil {
		t.Fatal("first upsert:", err)
	}

	// Upsert again with updated title
	patches[0].Title = "Test Patch v2 (updated)"
	patches[0].Severity = patchmgmt.SeverityCritical
	if err := repo.UpsertPatches(ctx, "dev-patch-3", patches); err != nil {
		t.Fatal("second upsert:", err)
	}

	// Should still be 1 patch, with updated data
	listed, err := repo.ListDevicePatches(ctx, "dev-patch-3", "")
	if err != nil {
		t.Fatal("list:", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 patch after idempotent upsert, got %d", len(listed))
	}
	if listed[0].Title != "Test Patch v2 (updated)" {
		t.Fatalf("expected updated title, got %s", listed[0].Title)
	}
	if listed[0].Severity != patchmgmt.SeverityCritical {
		t.Fatalf("expected critical severity, got %s", listed[0].Severity)
	}
	t.Log("Upsert idempotency verified: same patch_id updated in place")
}

// TestPatchManagement_ScanReportStampsDevice is the end-to-end version of the
// empty-state fix: a device that reports a scan must come back carrying when it
// was scanned, and one that never has must not. The console renders "no pending
// updates" and "never scanned" off exactly this field, and the field is only
// worth having if the report path writes it.
func TestPatchManagement_ScanReportStampsDevice(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := patchmgmt.NewRepository(database)
	devices := devicemgmt.NewRepository(database)
	ctx := context.Background()

	createTestDeviceForPatch(t, database, "dev-stamp", "IDEM-STAMP")
	createTestDeviceForPatch(t, database, "dev-never", "IDEM-NEVER")

	for _, id := range []string{"dev-stamp", "dev-never"} {
		if _, err := database.ExecContext(ctx,
			`INSERT INTO device_patches
				(id, device_id, patch_id, title, description, severity, category,
				 kb_id, size_bytes, installed_state, reboot_required, discovered_at, updated_at)
			 VALUES (?, ?, ?, 'x', '', 'low', 'updates', '', 0, 'missing', 0, ?, ?)`,
			"row-"+id, id, "KB0000000", time.Now().UTC(), time.Now().UTC(),
		); err != nil {
			t.Fatalf("seed patch for %s: %v", id, err)
		}
	}

	before := time.Now().UTC().Add(-time.Minute)

	// What reportScanResults does on accepting a report.
	if err := repo.UpsertPatches(ctx, "dev-stamp", []patchmgmt.DevicePatch{
		{PatchID: "KB5007652", Title: "b", Severity: patchmgmt.SeverityLow,
			Category: patchmgmt.CategoryUpdates, InstalledState: patchmgmt.StateMissing},
	}); err != nil {
		t.Fatal("upsert:", err)
	}
	if err := devices.UpdateLastPatchScan(ctx, "dev-stamp", time.Now().UTC()); err != nil {
		t.Fatal("stamp:", err)
	}

	scanned, err := devices.GetByID(ctx, "dev-stamp")
	if err != nil {
		t.Fatal("get scanned device:", err)
	}
	if scanned.LastPatchScanAt == nil {
		t.Error("LastPatchScanAt is nil for a device that just reported a scan; the console would render it as never scanned")
	} else if scanned.LastPatchScanAt.Before(before) {
		t.Errorf("LastPatchScanAt = %v, want at or after %v", scanned.LastPatchScanAt, before)
	}

	// The device that was never scanned must stay NULL -- not defaulted to the
	// row's discovery time, which would date an unexamined machine as examined.
	never, err := devices.GetByID(ctx, "dev-never")
	if err != nil {
		t.Fatal("get unscanned device:", err)
	}
	if never.LastPatchScanAt != nil {
		t.Errorf("LastPatchScanAt = %v, want nil: this device never reported a scan", never.LastPatchScanAt)
	}

	// And the scanned device's superseded row is no longer pending.
	pending, err := repo.ListDevicePatches(ctx, "dev-stamp", patchmgmt.StateMissing)
	if err != nil {
		t.Fatal("list pending:", err)
	}
	if len(pending) != 1 || pending[0].PatchID != "KB5007652" {
		t.Fatalf("pending = %+v, want only KB5007652", pending)
	}
}

func createTestDeviceForPatch(t *testing.T, database *sqlx.DB, id, hostname string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := database.Exec(`INSERT INTO devices (id, hostname, os_name, device_secret_hash, status, enrolled_at, last_seen_at, updated_at, created_at)
		VALUES (?, ?, 'windows', 'hash123', 'online', ?, ?, ?, ?)`, id, hostname, now, now, now, now)
	if err != nil {
		t.Fatalf("create test device %s: %v", id, err)
	}
}

func createTestUserForPatch(t *testing.T, database *sqlx.DB, id, username, role string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := database.Exec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at)
		VALUES (?, ?, '$2a$10$dummy', ?, ?, ?)`, id, username, role, now, now)
	if err != nil {
		t.Fatalf("create test user %s: %v", id, err)
	}
}

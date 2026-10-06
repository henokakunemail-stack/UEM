package patchmgmt

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/jmoiron/sqlx"
)

func newTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func seedDevice(t *testing.T, d *sqlx.DB, id, hostname string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := d.Exec(`
		INSERT INTO devices (id, hostname, os_name, status, enrolled_at,
		                     device_secret_hash, created_at, updated_at)
		VALUES (?, ?, 'windows', 'online', ?, 'secret_hash', ?, ?)`,
		id, hostname, now, now, now)
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

// seedPatch writes a row directly, standing in for a scan that reported it on
// some earlier day.
func seedPatch(t *testing.T, d *sqlx.DB, deviceID, patchID, state string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := d.Exec(`
		INSERT INTO device_patches
			(id, device_id, patch_id, title, description, severity, category,
			 kb_id, size_bytes, installed_state, reboot_required, discovered_at, updated_at)
		VALUES (?, ?, ?, ?, '', 'important', 'security', ?, 0, ?, 0, ?, ?)`,
		NewID(), deviceID, patchID, "Some update", patchID, state, now, now)
	if err != nil {
		t.Fatalf("seed patch %s: %v", patchID, err)
	}
}

func stateOf(t *testing.T, d *sqlx.DB, deviceID, patchID string) string {
	t.Helper()
	var state string
	err := d.Get(&state,
		`SELECT installed_state FROM device_patches WHERE device_id = ? AND patch_id = ?`,
		deviceID, patchID)
	if err != nil {
		t.Fatalf("read state of %s: %v", patchID, err)
	}
	return state
}

// A superseded update has to stop counting. KB5007651 is the row this whole
// reconciliation exists for: Windows Update stopped offering it, nothing removed
// it, and the console went on listing a KB the machine could no longer fetch.
func TestUpsertSettlesPatchesTheScanNoLongerReports(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)

	seedDevice(t, d, "dev1", "PC-1")
	seedPatch(t, d, "dev1", "KB5007651", StateMissing)
	seedPatch(t, d, "dev1", "KB5007652", StateMissing)
	seedPatch(t, d, "dev1", "KB5007653", StateMissing)
	seedPatch(t, d, "dev1", "KB5007654", StateMissing)

	err := repo.UpsertPatches(ctx, "dev1", []DevicePatch{
		{PatchID: "KB5007652", Title: "b", Severity: SeverityImportant, Category: CategorySecurity, InstalledState: StateMissing},
		{PatchID: "KB5007653", Title: "c", Severity: SeverityImportant, Category: CategorySecurity, InstalledState: StateMissing},
		{PatchID: "KB5007654", Title: "d", Severity: SeverityImportant, Category: CategorySecurity, InstalledState: StateMissing},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if got := stateOf(t, d, "dev1", "KB5007651"); got != StateInstalled {
		t.Errorf("KB5007651 state = %q, want %q: the scan dropped it, so it is no longer pending", got, StateInstalled)
	}
	for _, stillPending := range []string{"KB5007652", "KB5007653", "KB5007654"} {
		if got := stateOf(t, d, "dev1", stillPending); got != StateMissing {
			t.Errorf("%s state = %q, want %q: the scan still reports it", stillPending, got, StateMissing)
		}
	}
}

// Reconciliation is scoped to the device that was scanned. Another device's rows
// must survive, including a row for a KB this device just dropped.
func TestUpsertSettlesOnlyTheScannedDevice(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)

	seedDevice(t, d, "dev1", "PC-1")
	seedDevice(t, d, "dev2", "PC-2")
	seedPatch(t, d, "dev2", "KB5007651", StateMissing)
	seedPatch(t, d, "dev1", "KB5007652", StateMissing)

	err := repo.UpsertPatches(ctx, "dev1", []DevicePatch{
		{PatchID: "KB5007652", Title: "b", Severity: SeverityLow, Category: CategoryUpdates, InstalledState: StateMissing},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if got := stateOf(t, d, "dev2", "KB5007651"); got != StateMissing {
		t.Errorf("dev2 KB5007651 state = %q, want %q: this device was never scanned", got, StateMissing)
	}
}

// An empty scan must not reach SQL as NOT IN (), which SQLite rejects. This is
// the common case -- every fully patched device reports an empty list on every
// scan -- so a syntax error here would fail the scan of a healthy machine.
//
// It also pins the deliberate limit: an empty scan settles nothing. See
// reconcileAbsentPatches for why that is the safe side of the trade.
func TestUpsertWithEmptyScanDoesNotBuildAnEmptyInClause(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)

	seedDevice(t, d, "dev1", "PC-1")
	seedPatch(t, d, "dev1", "KB5007651", StateMissing)

	if err := repo.UpsertPatches(ctx, "dev1", nil); err != nil {
		t.Fatalf("an empty scan must succeed, got: %v", err)
	}
	if got := stateOf(t, d, "dev1", "KB5007651"); got != StateMissing {
		t.Errorf("KB5007651 state = %q, want %q: an empty scan leaves rows alone", got, StateMissing)
	}
}

// Two updates can report the same patch_id in one scan. NOT IN with a repeated
// element is legal SQL, but the dedupe keeps the argument list from growing with
// the size of a list nobody reads.
func TestUpsertWithDuplicatePatchIDsInOneScan(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)

	seedDevice(t, d, "dev1", "PC-1")
	seedPatch(t, d, "dev1", "KB5007651", StateMissing)
	seedPatch(t, d, "dev1", "KB5007652", StateMissing)

	err := repo.UpsertPatches(ctx, "dev1", []DevicePatch{
		{PatchID: "KB5007652", Title: "b", Severity: SeverityLow, Category: CategoryUpdates, InstalledState: StateMissing},
		{PatchID: "KB5007652", Title: "b", Severity: SeverityLow, Category: CategoryUpdates, InstalledState: StateMissing},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if got := stateOf(t, d, "dev1", "KB5007651"); got != StateInstalled {
		t.Errorf("KB5007651 state = %q, want %q", got, StateInstalled)
	}
	if got := stateOf(t, d, "dev1", "KB5007652"); got != StateMissing {
		t.Errorf("KB5007652 state = %q, want %q", got, StateMissing)
	}
}

package assetlicense

import (
	"context"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

func seedSoftware(t *testing.T, d *sqlx.DB, deviceID, softwareJSON string) {
	t.Helper()
	now := time.Now().UTC()
	d.MustExec(`INSERT INTO device_inventory (device_id, hw, software, os_detail, collected_at, updated_at)
		VALUES (?, '{}', ?, '{}', ?, ?)`, deviceID, softwareJSON, now, now)
}

func compliance(t *testing.T, d *sqlx.DB, name string) *LicenseComplianceSummary {
	t.Helper()
	list, err := NewRepository(d).ComputeCompliance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.SoftwareName == name {
			return c
		}
	}
	t.Fatalf("no compliance row for %q", name)
	return nil
}

// The installed count was a LIKE over the whole software JSON: a license
// name matched publishers and versions, % and _ were wildcards, and retired
// devices counted.
func TestInstalledCountMatchesProgramNamesOnly(t *testing.T) {
	d, _ := assetFixture(t)
	repo := NewRepository(d)
	for _, id := range []string{"a", "b", "c"} {
		seedDevice(t, d, id, id)
	}
	d.MustExec(`UPDATE devices SET retired_at = ? WHERE id = 'c'`, time.Now().UTC())
	seedSoftware(t, d, "a", `[{"name":"Microsoft Office Professional Plus 2021","version":"16.0","publisher":"Microsoft Corporation"}]`)
	seedSoftware(t, d, "b", `[{"name":"Visual Studio Code","version":"1.95","publisher":"Microsoft Corporation"}]`)
	seedSoftware(t, d, "c", `[{"name":"Microsoft Office Professional Plus 2021","version":"16.0","publisher":"Microsoft Corporation"}]`)
	for _, name := range []string{"Microsoft Office", "Microsoft", "100%"} {
		if err := repo.CreateLicense(context.Background(), &SoftwareLicense{SoftwareName: name, LicenseType: "perpetual", TotalSeats: 5}); err != nil {
			t.Fatal(err)
		}
	}

	if got := compliance(t, d, "Microsoft Office").InstalledDetected; got != 1 {
		t.Errorf("Microsoft Office installs = %d, want 1 (the retired device does not count)", got)
	}
	if got := compliance(t, d, "Microsoft").InstalledDetected; got != 1 {
		t.Errorf("Microsoft installs = %d, want 1 (a publisher is not a program name)", got)
	}
	if got := compliance(t, d, "100%").InstalledDetected; got != 0 {
		t.Errorf("100%% installs = %d, want 0 (%% is not a wildcard)", got)
	}
}

// An allocation to a retired device holds no seat.
func TestAllocationsToRetiredDevicesHoldNoSeat(t *testing.T) {
	d, _ := assetFixture(t)
	repo := NewRepository(d)
	seedDevice(t, d, "a", "a")
	seedDevice(t, d, "c", "c")
	lic := &SoftwareLicense{SoftwareName: "Tool", LicenseType: "perpetual", TotalSeats: 5}
	if err := repo.CreateLicense(context.Background(), lic); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	d.MustExec(`INSERT INTO license_allocations (id, license_id, device_id, allocated_at, allocated_by) VALUES ('x1', ?, 'a', ?, 'u'), ('x2', ?, 'c', ?, 'u')`, lic.ID, now, lic.ID, now)
	d.MustExec(`UPDATE devices SET retired_at = ? WHERE id = 'c'`, now)
	if got := compliance(t, d, "Tool").AllocatedSeats; got != 1 {
		t.Fatalf("allocated seats = %d, want 1", got)
	}
}

// A failed read used to become 0 installs, which reads as compliant.
func TestAFailedInventoryReadIsAnError(t *testing.T) {
	d, _ := assetFixture(t)
	repo := NewRepository(d)
	if err := repo.CreateLicense(context.Background(), &SoftwareLicense{SoftwareName: "Tool", LicenseType: "perpetual", TotalSeats: 5}); err != nil {
		t.Fatal(err)
	}
	d.MustExec(`ALTER TABLE device_inventory RENAME TO device_inventory_gone`)
	if _, err := repo.ComputeCompliance(context.Background()); err == nil {
		t.Fatal("compliance computed without the inventory")
	}
}

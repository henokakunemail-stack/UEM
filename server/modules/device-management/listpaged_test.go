package devicemanagement

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// newListTestDB mirrors newTestDB in inventory_test.go: a migrated SQLite DB in
// a temp dir, isolated per test.
func newListTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "list.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedListDevice(t *testing.T, d *sqlx.DB, id, hostname, status string, retired bool) {
	t.Helper()
	var retiredAt any
	if retired {
		retiredAt = "2026-01-01 00:00:00"
	}
	_, err := d.Exec(`
		INSERT INTO devices (id, hostname, os_name, status, enrolled_at, device_secret_hash, created_at, updated_at, retired_at)
		VALUES (?, ?, 'windows', ?, '2026-01-01 00:00:00', 'x', '2026-01-01 00:00:00', '2026-01-01 00:00:00', ?)`,
		id, hostname, status, retiredAt)
	if err != nil {
		t.Fatalf("seed device %s: %v", id, err)
	}
}

// Retirement is a column (retired_at), not a status value, and the dashboard
// counts retired endpoints as `retired_at IS NOT NULL`. ListPaged used to
// default to `retired_at IS NULL` and then AND `status = 'retired'`, so the
// console's "Retired Only" filter and the dashboard's retired KPI link always
// returned zero rows however many devices had been decommissioned.
func TestListPagedRetiredFilter(t *testing.T) {
	d := newListTestDB(t)
	ctx := context.Background()

	seedListDevice(t, d, "d1", "pc-online", "online", false)
	seedListDevice(t, d, "d2", "pc-offline", "offline", false)
	seedListDevice(t, d, "d3", "pc-retired-online", "online", true)
	seedListDevice(t, d, "d4", "pc-retired-offline", "offline", true)

	repo := NewRepository(d)

	t.Run("default excludes retired", func(t *testing.T) {
		rows, total, err := repo.ListPaged(ctx, "", "", 50, 0)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 2 || len(rows) != 2 {
			t.Fatalf("want 2 active devices, got total=%d len=%d", total, len(rows))
		}
	})

	t.Run("status=retired returns retired devices", func(t *testing.T) {
		rows, total, err := repo.ListPaged(ctx, "retired", "", 50, 0)
		if err != nil {
			t.Fatalf("list retired: %v", err)
		}
		if total != 2 || len(rows) != 2 {
			t.Fatalf("want 2 retired devices, got total=%d len=%d", total, len(rows))
		}
		for _, r := range rows {
			if r.RetiredAt == nil {
				t.Errorf("device %s returned under status=retired but is not retired", r.ID)
			}
		}
	})

	t.Run("status=online excludes retired", func(t *testing.T) {
		rows, total, err := repo.ListPaged(ctx, "online", "", 50, 0)
		if err != nil {
			t.Fatalf("list online: %v", err)
		}
		if total != 1 || len(rows) != 1 || rows[0].ID != "d1" {
			t.Fatalf("want only d1, got total=%d rows=%v", total, ids(rows))
		}
	})

	t.Run("retired filter still honours site", func(t *testing.T) {
		// Site is ANDed on top of whichever predicate is active, so a retired
		// device at another site must not leak into the result.
		_, err := d.Exec(`UPDATE devices SET site = 'jakarta' WHERE id = 'd3'`)
		if err != nil {
			t.Fatalf("set site: %v", err)
		}
		rows, total, err := repo.ListPaged(ctx, "retired", "jakarta", 50, 0)
		if err != nil {
			t.Fatalf("list retired+jakarta: %v", err)
		}
		if total != 1 || len(rows) != 1 || rows[0].ID != "d3" {
			t.Fatalf("want only d3, got total=%d rows=%v", total, ids(rows))
		}
	})
}

func ids(rows []Device) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

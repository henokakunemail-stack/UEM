package unit

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// newTestDB returns a migrated SQLite DB in a temp dir. Each test gets its own
// isolated database — no shared state, no cleanup of a production file.
func newTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestDeviceEnrollmentFlow(t *testing.T) {
	d := newTestDB(t)
	repo := devicemgmt.NewRepository(d)
	ctx := context.Background()

	// Step 1: admin pre-registers the device with a one-time token.
	plain := devicemgmt.GenerateToken()
	now := frozenNow()
	tokenHash := devicemgmt.HashToken(plain) // consumed to NULL on enrollment
	site := "cabang-surabaya"
	dev := devicemgmt.Device{
		ID:                  devicemgmt.NewID(),
		Hostname:            "PC-CABANG-01",
		OSName:              devicemgmt.OSWindows,
		Status:              devicemgmt.StatusOffline,
		EnrolledAt:          now,
		EnrollmentTokenHash: &tokenHash,
		Site:                &site,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := repo.Create(ctx, dev); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Step 2: agent presents the token and receives a persistent secret.
	secret := devicemgmt.GenerateToken()
	if err := repo.ConsumeEnrollmentToken(ctx, devicemgmt.HashToken(plain), devicemgmt.HashToken(secret)); err != nil {
		t.Fatalf("consume token: %v", err)
	}

	// Step 3: the agent authenticates its connection by the stored secret hash.
	got, err := repo.FindBySecretHash(ctx, devicemgmt.HashToken(secret))
	if err != nil {
		t.Fatalf("find by secret: %v", err)
	}
	if got.ID != dev.ID || got.Hostname != "PC-CABANG-01" {
		t.Fatalf("unexpected device: %+v", got)
	}

	// Step 4: replaying the one-time token must fail — it was consumed.
	if err := repo.ConsumeEnrollmentToken(ctx, devicemgmt.HashToken(plain), devicemgmt.HashToken("x")); !errors.Is(err, devicemgmt.ErrNotFound) {
		t.Fatalf("replay of consumed token: expected ErrNotFound, got %v", err)
	}
	// And an unknown token must fail too.
	if err := repo.ConsumeEnrollmentToken(ctx, devicemgmt.HashToken("bogus"), devicemgmt.HashToken("x")); !errors.Is(err, devicemgmt.ErrNotFound) {
		t.Fatalf("unknown token: expected ErrNotFound, got %v", err)
	}
}

func TestDeviceStatusTransitions(t *testing.T) {
	d := newTestDB(t)
	repo := devicemgmt.NewRepository(d)
	ctx := context.Background()

	dev := devicemgmt.Device{
		ID: devicemgmt.NewID(), Hostname: "PC-02", OSName: devicemgmt.OSLinux,
		Status: devicemgmt.StatusOffline, EnrolledAt: frozenNow(),
		DeviceSecretHash: devicemgmt.HashToken("s"), CreatedAt: frozenNow(), UpdatedAt: frozenNow(),
	}
	if err := repo.Create(ctx, dev); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.UpdateStatus(ctx, dev.ID, devicemgmt.StatusOnline, frozenNow()); err != nil {
		t.Fatalf("update status online: %v", err)
	}
	got, _ := repo.GetByID(ctx, dev.ID)
	if got.Status != devicemgmt.StatusOnline {
		t.Fatalf("expected online, got %q", got.Status)
	}

	online, err := repo.List(ctx, devicemgmt.StatusOnline, "")
	if err != nil {
		t.Fatalf("list online: %v", err)
	}
	if len(online) != 1 {
		t.Fatalf("expected 1 online device, got %d", len(online))
	}
	offline, _ := repo.List(ctx, devicemgmt.StatusOffline, "")
	if len(offline) != 0 {
		t.Fatalf("expected 0 offline, got %d", len(offline))
	}

	if err := repo.UpdateStatus(ctx, "nonexistent", devicemgmt.StatusOnline, frozenNow()); !errors.Is(err, devicemgmt.ErrNotFound) {
		t.Fatalf("update nonexistent: expected ErrNotFound, got %v", err)
	}
}

func TestDeviceListFilteringBySite(t *testing.T) {
	d := newTestDB(t)
	repo := devicemgmt.NewRepository(d)
	ctx := context.Background()

	for i, site := range []string{"pusat", "cabang-a", "cabang-a"} {
		now := frozenNow()
		if err := repo.Create(ctx, devicemgmt.Device{
			ID: devicemgmt.NewID(), Hostname: "PC", OSName: devicemgmt.OSWindows,
			Status: devicemgmt.StatusOffline, EnrolledAt: now, Site: &site,
			DeviceSecretHash: devicemgmt.HashToken("s"), CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if n := len(mustList(t, repo, "", "")); n != 3 {
		t.Fatalf("expected 3 total, got %d", n)
	}
	if n := len(mustList(t, repo, "", "cabang-a")); n != 2 {
		t.Fatalf("expected 2 in cabang-a, got %d", n)
	}
	if n := len(mustList(t, repo, "", "pusat")); n != 1 {
		t.Fatalf("expected 1 in pusat, got %d", n)
	}
}

func mustList(t *testing.T, repo *devicemgmt.Repository, status, site string) []devicemgmt.Device {
	t.Helper()
	got, err := repo.List(context.Background(), status, site)
	if err != nil {
		t.Fatalf("list(%q,%q): %v", status, site, err)
	}
	return got
}

// frozenNow is a fixed timestamp for deterministic tests.
func frozenNow() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) }

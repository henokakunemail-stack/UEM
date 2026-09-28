package transport

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/logger"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

func init() {
	logger.Init("disabled", "")
}

func TestHeartbeatFlusher_BatchExecution(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "flusher_test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	repo := devicemgmt.NewRepository(d)
	ctx := context.Background()
	now := time.Now().UTC()

	// Seed 3 devices
	for i := 1; i <= 3; i++ {
		id := devicemgmt.NewID()
		d := devicemgmt.Device{
			ID:         id,
			Hostname:   "FLUSH-TEST-" + id[:4],
			OSName:     devicemgmt.OSLinux,
			Status:     devicemgmt.StatusOffline,
			EnrolledAt: now,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := repo.Create(ctx, d); err != nil {
			t.Fatalf("create device: %v", err)
		}
	}

	devices, err := repo.List(ctx, "", "")
	if err != nil || len(devices) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(devices))
	}

	flusher := NewHeartbeatFlusher(d, 50*time.Millisecond)
	defer flusher.Close()

	// Record heartbeats for all 3 devices
	for _, dev := range devices {
		flusher.Record(dev.ID)
	}

	if flusher.PendingCount() != 3 {
		t.Fatalf("expected 3 pending, got %d", flusher.PendingCount())
	}

	// Trigger flush
	flusher.Flush()

	if flusher.PendingCount() != 0 {
		t.Fatalf("expected 0 pending after flush, got %d", flusher.PendingCount())
	}

	// Verify all 3 devices are now online
	for _, dev := range devices {
		updated, err := repo.GetByID(ctx, dev.ID)
		if err != nil {
			t.Fatalf("get device %s: %v", dev.ID, err)
		}
		if updated.Status != devicemgmt.StatusOnline {
			t.Errorf("device %s status = %q, want online", dev.ID, updated.Status)
		}
	}
}

func TestHeartbeatFlusher_RemovePreventsStaleOverwrite(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "flusher_remove_test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	repo := devicemgmt.NewRepository(d)
	ctx := context.Background()
	now := time.Now().UTC()

	id := devicemgmt.NewID()
	device := devicemgmt.Device{
		ID:         id,
		Hostname:   "FLUSH-REMOVE-01",
		OSName:     devicemgmt.OSWindows,
		Status:     devicemgmt.StatusOffline,
		EnrolledAt: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := repo.Create(ctx, device); err != nil {
		t.Fatalf("create device: %v", err)
	}

	flusher := NewHeartbeatFlusher(d, 1*time.Second)
	defer flusher.Close()

	flusher.Record(id)
	if flusher.PendingCount() != 1 {
		t.Fatalf("expected 1 pending, got %d", flusher.PendingCount())
	}

	// Remove device (e.g. on disconnect)
	flusher.Remove(id)
	if flusher.PendingCount() != 0 {
		t.Fatalf("expected 0 pending after remove, got %d", flusher.PendingCount())
	}

	flusher.Flush()

	// Device should remain offline
	check, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if check.Status != devicemgmt.StatusOffline {
		t.Fatalf("expected offline, got %s", check.Status)
	}
}

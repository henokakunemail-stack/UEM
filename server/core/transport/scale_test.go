package transport

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/logger"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// TestHeartbeatFlusher_Scale10KConcurrentAgents is the production readiness
// proof for the "500 to 10,000+ endpoints" claim. It seeds a realistic fleet,
// drives heartbeats from many concurrent agent goroutines exactly as the
// WebSocket hub does, and asserts the flusher drains every device with no lost
// updates and no write-lock failure.
func TestHeartbeatFlusher_Scale10KConcurrentAgents(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "scale_test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	const fleetSize = 10000

	// Seed the fleet through the repository so every NOT NULL column and
	// default is populated exactly as a real enrollment would.
	repo := devicemgmt.NewRepository(d)
	ids := make([]string, fleetSize)
	for i := 0; i < fleetSize; i++ {
		id := devicemgmt.NewID()
		ids[i] = id
		osVersion := "12.04"
		device := devicemgmt.Device{
			ID:         id,
			Hostname:   fmt.Sprintf("SCALE-%05d", i),
			OSName:     devicemgmt.OSLinux,
			OSVersion:  &osVersion,
			Status:     devicemgmt.StatusOffline,
			EnrolledAt: now,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := repo.Create(ctx, device); err != nil {
			t.Fatalf("seed device %d: %v", i, err)
		}
	}

	t.Logf("seeded %d devices", fleetSize)

	// Flusher in production mode (2s interval), as wired in NewWSHandler.
	flusher := NewHeartbeatFlusher(d, 2*time.Second)
	defer flusher.Close()

	// Simulate concurrent agent heartbeats: 200 goroutines, matching how the
	// hub calls Record from many simultaneous WebSocket read loops.
	const writers = 200
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < fleetSize; i += writers {
				flusher.Record(ids[i])
			}
		}(w)
	}
	wg.Wait()
	recordElapsed := time.Since(start)

	if got := flusher.PendingCount(); got != fleetSize {
		t.Fatalf("PendingCount = %d, want %d", got, fleetSize)
	}
	t.Logf("%d concurrent heartbeats recorded in %v (%.0f/sec)",
		fleetSize, recordElapsed, float64(fleetSize)/recordElapsed.Seconds())

	// Flush the full fleet in one batched transaction.
	flushStart := time.Now()
	flusher.Flush()
	flushElapsed := time.Since(flushStart)

	if got := flusher.PendingCount(); got != 0 {
		t.Fatalf("PendingCount after flush = %d, want 0", got)
	}
	t.Logf("flushed %d devices in %v (%.0f updates/sec)",
		fleetSize, flushElapsed, float64(fleetSize)/flushElapsed.Seconds())

	// Assert every single device transitioned to online. A partial flush is
	// exactly the silent data-loss bug this test exists to catch.
	var onlineCount int
	if err := d.GetContext(ctx, &onlineCount,
		`SELECT COUNT(*) FROM devices WHERE status = 'online'`); err != nil {
		t.Fatalf("count online: %v", err)
	}
	if onlineCount != fleetSize {
		t.Errorf("online devices = %d, want %d (lost %d heartbeats)",
			onlineCount, fleetSize, fleetSize-onlineCount)
	}

	// last_seen_at must be recent for every device, proving the flusher wrote
	// real values rather than silently skipping rows.
	var staleCount int
	if err := d.GetContext(ctx, &staleCount, `
		SELECT COUNT(*) FROM devices
		WHERE last_seen_at IS NULL OR last_seen_at < ?`, now.Add(-time.Hour)); err != nil {
		t.Fatalf("count stale: %v", err)
	}
	if staleCount != 0 {
		t.Errorf("%d devices have missing or stale last_seen_at", staleCount)
	}
}

// TestHeartbeatFlusher_SustainedHeartbeatCycles runs repeated heartbeat waves
// to prove the buffer does not grow without bound and the periodic flusher
// keeps up with production cadence.
func TestHeartbeatFlusher_SustainedHeartbeatCycles(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "sustained_test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	repo := devicemgmt.NewRepository(d)
	ctx := context.Background()
	now := time.Now().UTC()

	const fleetSize = 2000
	ids := make([]string, fleetSize)
	for i := 0; i < fleetSize; i++ {
		id := devicemgmt.NewID()
		ids[i] = id
		device := devicemgmt.Device{
			ID:         id,
			Hostname:   fmt.Sprintf("SUSTAIN-%05d", i),
			OSName:     devicemgmt.OSWindows,
			Status:     devicemgmt.StatusOffline,
			EnrolledAt: now,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := repo.Create(ctx, device); err != nil {
			t.Fatalf("create device %d: %v", i, err)
		}
	}

	// 100ms interval stands in for the 2s production interval so the test
	// exercises many flush cycles without a multi-minute wall clock.
	flusher := NewHeartbeatFlusher(d, 100*time.Millisecond)
	defer flusher.Close()

	const waves = 5
	for w := 0; w < waves; w++ {
		for _, id := range ids {
			flusher.Record(id)
		}
		// Let the background loop flush this wave.
		deadline := time.Now().Add(5 * time.Second)
		for flusher.PendingCount() > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := flusher.PendingCount(); got != 0 {
			t.Fatalf("wave %d: buffer did not drain, %d pending", w, got)
		}
	}

	var onlineCount int
	if err := d.GetContext(ctx, &onlineCount,
		`SELECT COUNT(*) FROM devices WHERE status = 'online'`); err != nil {
		t.Fatalf("count online: %v", err)
	}
	if onlineCount != fleetSize {
		t.Errorf("online devices = %d, want %d", onlineCount, fleetSize)
	}
	t.Logf("%d waves x %d devices drained cleanly, buffer bounded", waves, fleetSize)
}

func init() {
	// Errors stay rendered. At "disabled" zerolog's constructor discards every
	// log.Error, which is exactly the level that hid the batch-loss bug: the
	// flusher failed, said nothing, and the failure was found only by an
	// unrelated count assertion. A noisy suite is cheaper than a silent one.
	logger.Init("error", "")
}

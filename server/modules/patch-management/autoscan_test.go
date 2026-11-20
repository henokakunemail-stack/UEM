package patchmgmt

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

type recHub struct {
	mu      sync.Mutex
	online  map[string]bool
	sent    []string
	refuses map[string]bool
}

func (h *recHub) Online(id string) bool { return h.online[id] }
func (h *recHub) SendTo(id string, b []byte) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	var env struct{ Command string }
	_ = json.Unmarshal(b, &env)
	if env.Command != "patch.scan" || h.refuses[id] {
		return false
	}
	h.sent = append(h.sent, id)
	return true
}

func seedScanDevice(t *testing.T, d *sqlx.DB, id, status, caps string, lastScan *time.Time, retired bool) {
	t.Helper()
	now := time.Now().UTC()
	var ret any
	if retired {
		ret = now
	}
	d.MustExec(`INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status, device_secret_hash,
		enrolled_at, created_at, updated_at, capabilities, last_patch_scan_at, retired_at)
		VALUES (?, ?, 'windows', '11', '1.6.3', ?, 'h', ?, ?, ?, ?, ?, ?)`,
		id, "H-"+id, status, now, now, now, caps, lastScan, ret)
}

const scanCaps = `["ping","patch.scan"]`

func TestAutoScanPicksOnlyDueOnlineCapableDevices(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	fresh := now.Add(-2 * time.Hour)
	stale := now.Add(-30 * time.Hour)

	seedScanDevice(t, d, "never", "online", scanCaps, nil, false)
	seedScanDevice(t, d, "stale", "online", scanCaps, &stale, false)
	seedScanDevice(t, d, "fresh", "online", scanCaps, &fresh, false)
	seedScanDevice(t, d, "offline", "offline", scanCaps, nil, false)
	seedScanDevice(t, d, "oldagent", "online", `["ping"]`, nil, false)
	seedScanDevice(t, d, "retired", "online", scanCaps, nil, true)
	seedScanDevice(t, d, "notonnode", "online", scanCaps, nil, false)

	hub := &recHub{online: map[string]bool{"never": true, "stale": true, "fresh": true, "offline": true,
		"oldagent": true, "retired": true}}
	a := &AutoScan{DB: d, Hub: hub, Interval: 24 * time.Hour, Now: func() time.Time { return now }}

	got := a.RunOnce(context.Background())
	sort.Strings(got)
	if len(got) != 2 || got[0] != "never" || got[1] != "stale" {
		t.Fatalf("sent to %v, want [never stale]", got)
	}

	// The agent has not reported yet: a second tick must not send again.
	if again := a.RunOnce(context.Background()); len(again) != 0 {
		t.Fatalf("re-sent to %v before the interval passed", again)
	}

	// After a full interval with still no report, it is due again.
	now = now.Add(25 * time.Hour)
	if again := a.RunOnce(context.Background()); len(again) == 0 {
		t.Fatal("a device that never reported was not retried after the interval")
	}
}

func TestAutoScanRespectsBatchAndDisable(t *testing.T) {
	d := newTestDB(t)
	hub := &recHub{online: map[string]bool{}}
	for _, id := range []string{"a", "b", "c", "e", "f"} {
		seedScanDevice(t, d, id, "online", scanCaps, nil, false)
		hub.online[id] = true
	}
	a := &AutoScan{DB: d, Hub: hub, Interval: time.Hour, Batch: 2}
	if got := a.RunOnce(context.Background()); len(got) != 2 {
		t.Fatalf("batch of 2 sent %d", len(got))
	}
	off := &AutoScan{DB: d, Hub: hub, Interval: 0}
	if got := off.RunOnce(context.Background()); got != nil {
		t.Fatalf("disabled autoscan sent %v", got)
	}
}

// A send the hub refused is not recorded as dispatched, so the next tick
// tries again instead of waiting a whole interval.
func TestAutoScanRetriesARefusedSend(t *testing.T) {
	d := newTestDB(t)
	seedScanDevice(t, d, "x", "online", scanCaps, nil, false)
	hub := &recHub{online: map[string]bool{"x": true}, refuses: map[string]bool{"x": true}}
	a := &AutoScan{DB: d, Hub: hub, Interval: time.Hour}
	if got := a.RunOnce(context.Background()); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	hub.refuses = nil
	if got := a.RunOnce(context.Background()); len(got) != 1 {
		t.Fatalf("refused send was not retried: %v", got)
	}
}

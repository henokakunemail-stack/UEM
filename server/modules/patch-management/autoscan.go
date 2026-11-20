package patchmgmt

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
)

// AutoScan sends patch.scan on a schedule.
//
// A scan used to happen only when someone pressed "Scan" on a device, so the
// pending-patch list for most of the fleet was whatever it was the last time
// anybody looked -- weeks old on some machines, never on others. A compliance
// report built on that cannot say whether a device is patched. AutoScan keeps
// every online device's scan no older than Interval.
//
// It is gentle on purpose. Each tick sends at most Batch scans, so a fleet of
// a few hundred PCs coming online at 08:00 does not hit Windows Update / WSUS
// all in the same minute, and a device that took a scan but never reported
// back is not sent another one until Interval has passed again.
type AutoScan struct {
	DB       *sqlx.DB
	Hub      HubDispatcher
	Interval time.Duration // 0 disables
	Tick     time.Duration
	Batch    int
	Now      func() time.Time

	mu         sync.Mutex
	dispatched map[string]time.Time
}

// AutoScanTick and AutoScanBatch are the production pacing: 20 devices every
// 5 minutes covers 240 devices an hour.
const (
	AutoScanTick  = 5 * time.Minute
	AutoScanBatch = 20
)

// dueQuery lists online, non-retired devices whose agent can scan and whose
// last scan is missing or older than the cutoff, oldest first so a device
// never scanned goes before one that is merely due.
const dueQuery = `
	SELECT id FROM devices
	WHERE retired_at IS NULL
	  AND status = 'online'
	  AND capabilities LIKE '%"patch.scan"%'
	  AND (last_patch_scan_at IS NULL OR last_patch_scan_at < ?)
	ORDER BY CASE WHEN last_patch_scan_at IS NULL THEN 0 ELSE 1 END, last_patch_scan_at
	LIMIT ?`

// RunOnce sends one batch and returns the device IDs it sent to.
func (a *AutoScan) RunOnce(ctx context.Context) []string {
	if a.Interval <= 0 || a.Hub == nil {
		return nil
	}
	now := a.now()
	batch := a.Batch
	if batch <= 0 {
		batch = AutoScanBatch
	}
	// Over-fetch: some candidates are skipped below as recently dispatched or
	// not actually connected to this node.
	var ids []string
	if err := a.DB.SelectContext(ctx, &ids, dueQuery, now.Add(-a.Interval), batch*5); err != nil {
		log.Warn().Err(err).Msg("patch autoscan: list due devices")
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dispatched == nil {
		a.dispatched = map[string]time.Time{}
	}
	for id, at := range a.dispatched {
		if now.Sub(at) >= a.Interval {
			delete(a.dispatched, id)
		}
	}

	var sent []string
	for _, id := range ids {
		if len(sent) >= batch {
			break
		}
		if _, recent := a.dispatched[id]; recent {
			continue
		}
		if !a.Hub.Online(id) {
			continue
		}
		if !sendScan(a.Hub, id) {
			continue
		}
		a.dispatched[id] = now
		sent = append(sent, id)
	}
	if len(sent) > 0 {
		log.Info().Int("devices", len(sent)).Msg("patch autoscan: scans dispatched")
	}
	return sent
}

// Start runs RunOnce every Tick until ctx ends.
func (a *AutoScan) Start(ctx context.Context) {
	if a.Interval <= 0 {
		log.Info().Msg("patch autoscan disabled (PATCH_SCAN_INTERVAL=0)")
		return
	}
	tick := a.Tick
	if tick <= 0 {
		tick = AutoScanTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.RunOnce(ctx)
		}
	}
}

func (a *AutoScan) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now().UTC()
}

// sendScan delivers a patch.scan command to a connected device.
func sendScan(hub HubDispatcher, deviceID string) bool {
	b, err := json.Marshal(transport.Envelope{
		Type:    transport.TypeCommand,
		ID:      NewID(),
		Command: "patch.scan",
		Payload: map[string]any{},
	})
	if err != nil {
		return false
	}
	return hub.SendTo(deviceID, b)
}

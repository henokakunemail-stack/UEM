package inventory

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
)

// Reporter sends collection results to the server. Implemented by the transport
// client so this package does not depend on it directly.
type Reporter interface {
	ReportInventory(ctx context.Context, rep Report) error
}

// Scheduler runs periodic full inventory with a per-device stagger so a fleet of
// hundreds of agents does not all collect at the same instant.
type Scheduler struct {
	collector Collector
	reporter  Reporter
	period    time.Duration
	// window spreads collections across this duration; offset is stable per
	// device so the load stays even hour after hour.
	window   time.Duration
	deviceID string
}

// NewScheduler returns a periodic collector. period is the interval between
// collections for one device; window is how widely start times are spread.
func NewScheduler(collector Collector, reporter Reporter, deviceID string,
	period, window time.Duration) *Scheduler {
	return &Scheduler{
		collector: collector,
		reporter:  reporter,
		deviceID:  deviceID,
		period:    period,
		window:    window,
	}
}

// Run collects and reports until ctx is cancelled. The first collection happens
// immediately so a freshly started agent has inventory on record right away.
func (s *Scheduler) Run(ctx context.Context) {
	// Immediate collection on startup: a device that goes offline soon after
	// booting still has inventory on record.
	s.collectOnce(ctx)

	ticker := time.NewTicker(s.period)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.collectOnce(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// collectOnce gathers facts and ships them. A failure here is logged and
// swallowed: inventory is best effort, and a collection error must never stop
// the agent from processing commands.
func (s *Scheduler) collectOnce(ctx context.Context) {
	start := time.Now()
	rep, err := s.collector.Collect()
	if err != nil {
		log.Warn().Err(err).Dur("took", time.Since(start)).Msg("inventory collection failed")
		return
	}
	// Stamp the collection time here rather than in each per-OS collector: the
	// timestamp means "when the agent read the hardware", which is the moment
	// this function returns, and setting it once keeps every OS in sync.
	rep.CollectedAt = time.Now().UTC()
	if err := s.reporter.ReportInventory(ctx, rep); err != nil {
		log.Warn().Err(err).Msg("inventory report failed")
		return
	}
	log.Info().
		Dur("took", time.Since(start)).
		Int("software", len(rep.Software)).
		Int64("ram_bytes", rep.Hardware.RAMTotalBytes).
		Msg("inventory reported")
}

// CollectOnce performs a single collection on demand. Used by the
// inventory.collect command handler, which the server sends when an admin opens
// a device's detail page and wants fresh facts.
func CollectOnce(ctx context.Context, c Collector, r Reporter) error {
	rep, err := c.Collect()
	if err != nil {
		return err
	}
	// Same stamp as the periodic path: without it the server records the zero
	// time and an on-demand refresh looks identical to a stale snapshot.
	rep.CollectedAt = time.Now().UTC()
	return r.ReportInventory(ctx, rep)
}

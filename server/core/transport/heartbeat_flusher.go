package transport

import (
	"context"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"
)

// HeartbeatFlusher aggregates high-frequency agent heartbeats in memory
// and periodically writes them to SQLite inside a single database transaction.
// This prevents write-lock contention and disk I/O churn when managing 500 to 10,000+ endpoints.
type HeartbeatFlusher struct {
	db       *sqlx.DB
	interval time.Duration
	pending  map[string]time.Time
	mu       sync.Mutex
	// flushMu serialises the batch swap against itself. mu guards the map, but
	// it is released for the whole duration of the write, so without this a
	// second Flush could start a transaction while the first one holds the
	// write lock -- and the loser would drop its batch on the floor.
	flushMu sync.Mutex
	stop    chan struct{}
	done    chan struct{}
}

// NewHeartbeatFlusher creates and starts a background heartbeat flusher.
func NewHeartbeatFlusher(db *sqlx.DB, interval time.Duration) *HeartbeatFlusher {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	f := &HeartbeatFlusher{
		db:       db,
		interval: interval,
		pending:  make(map[string]time.Time),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go f.loop()
	return f
}

// Record queues a heartbeat timestamp for the specified device.
func (f *HeartbeatFlusher) Record(deviceID string) {
	if f == nil || deviceID == "" {
		return
	}
	f.mu.Lock()
	f.pending[deviceID] = time.Now().UTC()
	f.mu.Unlock()
}

// RecordWithTime queues a heartbeat timestamp with an explicit time.
func (f *HeartbeatFlusher) RecordWithTime(deviceID string, ts time.Time) {
	if f == nil || deviceID == "" {
		return
	}
	f.mu.Lock()
	f.pending[deviceID] = ts.UTC()
	f.mu.Unlock()
}

// Remove removes any queued heartbeat for the device so an offline transition
// is not overwritten by a delayed batch flush.
func (f *HeartbeatFlusher) Remove(deviceID string) {
	if f == nil || deviceID == "" {
		return
	}
	f.mu.Lock()
	delete(f.pending, deviceID)
	f.mu.Unlock()
}

// PendingCount returns the number of buffered heartbeats awaiting flush.
func (f *HeartbeatFlusher) PendingCount() int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending)
}

// Flush synchronously writes all buffered heartbeats to the database inside a single transaction.
//
// A failed flush is not a lost flush. The batch is handed back to pending so the
// next tick retries it, which means the only thing a write-lock collision can
// cost is a delayed online status -- never a permanently stale one. Dropping
// the batch instead would make a transient SQLITE_BUSY indistinguishable from
// ten thousand devices that stopped reporting.
func (f *HeartbeatFlusher) Flush() {
	if f == nil || f.db == nil {
		return
	}
	f.flushMu.Lock()
	defer f.flushMu.Unlock()

	f.mu.Lock()
	if len(f.pending) == 0 {
		f.mu.Unlock()
		return
	}
	batch := f.pending
	f.pending = make(map[string]time.Time, len(batch))
	f.mu.Unlock()

	timeout := 30 * time.Second
	if len(batch) > 1000 {
		timeout = time.Duration(len(batch))*5*time.Millisecond + 10*time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	now := time.Now().UTC()
	tx, err := f.db.BeginTxx(ctx, nil)
	if err != nil {
		log.Error().Err(err).Int("devices", len(batch)).Msg("heartbeat flusher: begin tx, batch requeued")
		f.requeue(batch)
		return
	}
	defer tx.Rollback()

	stmt, err := tx.PreparexContext(ctx, `
		UPDATE devices
		SET status = 'online', last_seen_at = ?, updated_at = ?
		WHERE id = ? AND retired_at IS NULL`)
	if err != nil {
		log.Error().Err(err).Int("devices", len(batch)).Msg("heartbeat flusher: prepare stmt, batch requeued")
		f.requeue(batch)
		return
	}
	defer stmt.Close()

	// A per-device failure is collected rather than logged and abandoned. The
	// transaction is rolled back on this path, so every device is either
	// written or retried; a partial commit would drop the failed ones with no
	// record that they were ever seen.
	var failed map[string]time.Time
	for id, lastSeen := range batch {
		if _, err := stmt.ExecContext(ctx, lastSeen, now, id); err != nil {
			log.Warn().Err(err).Str("device", id).Msg("heartbeat flusher: update device")
			if failed == nil {
				failed = make(map[string]time.Time, 1)
			}
			failed[id] = lastSeen
		}
	}
	if len(failed) > 0 {
		f.requeue(failed)
	}

	if err := tx.Commit(); err != nil {
		log.Error().Err(err).Int("devices", len(batch)).Msg("heartbeat flusher: commit batch, batch requeued")
		f.requeue(batch)
	}
}

// requeue returns a failed batch to pending so the next flush retries it.
//
// A heartbeat that arrived while the flush was in flight is newer than the one
// being requeued, so it wins: replaying the stale timestamp would move
// last_seen_at backwards and make a live device look like it went quiet during
// the retry.
//
// ponytail: Remove (a disconnect) cannot reach a batch that is already in
// flight, so a device that drops while a flush is failing is written online
// one extra time. That is bounded by the offline sweep, which marks it offline
// again on the next pass -- the same window the pre-requeue code already had.
// A tombstone set would close it exactly; add one if the sweep interval ever
// stops being the backstop.
func (f *HeartbeatFlusher) requeue(batch map[string]time.Time) {
	if f == nil || len(batch) == 0 {
		return
	}
	f.mu.Lock()
	for id, ts := range batch {
		if cur, ok := f.pending[id]; !ok || ts.After(cur) {
			f.pending[id] = ts
		}
	}
	f.mu.Unlock()
}

// Close gracefully flushes all pending heartbeats and terminates the background loop.
func (f *HeartbeatFlusher) Close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	select {
	case <-f.stop:
		f.mu.Unlock()
		return
	default:
		close(f.stop)
	}
	f.mu.Unlock()

	<-f.done
}

func (f *HeartbeatFlusher) loop() {
	defer close(f.done)
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			f.Flush()
		case <-f.stop:
			f.Flush()
			return
		}
	}
}

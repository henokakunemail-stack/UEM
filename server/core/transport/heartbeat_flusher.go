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
	stop     chan struct{}
	done     chan struct{}
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
func (f *HeartbeatFlusher) Flush() {
	if f == nil || f.db == nil {
		return
	}
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
		log.Error().Err(err).Msg("heartbeat flusher: begin tx")
		return
	}
	defer tx.Rollback()

	stmt, err := tx.PreparexContext(ctx, `
		UPDATE devices
		SET status = 'online', last_seen_at = ?, updated_at = ?
		WHERE id = ? AND retired_at IS NULL`)
	if err != nil {
		log.Error().Err(err).Msg("heartbeat flusher: prepare stmt")
		return
	}
	defer stmt.Close()

	for id, lastSeen := range batch {
		if _, err := stmt.ExecContext(ctx, lastSeen, now, id); err != nil {
			log.Warn().Err(err).Str("device", id).Msg("heartbeat flusher: update device")
		}
	}

	if err := tx.Commit(); err != nil {
		log.Error().Err(err).Msg("heartbeat flusher: commit batch")
	}
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

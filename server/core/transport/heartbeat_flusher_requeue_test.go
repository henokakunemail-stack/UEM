package transport

import (
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// closeDB returns a handle that fails every operation, which is the closest a
// test can get to the real failure: the write lock held by another writer for
// longer than _busy_timeout, so Begin fails and the batch has nowhere to go.
func closedDB(t *testing.T) *sqlx.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "gone.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return d
}

func seedDevice(t *testing.T, d *sqlx.DB, id string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := d.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, status,
			device_secret_hash, enrolled_at, created_at, updated_at)
		VALUES (?, ?, 'linux', '12.04', 'offline', 'hash', ?, ?, ?)`,
		id, id, now, now, now); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func onlineCount(t *testing.T, d *sqlx.DB) int {
	t.Helper()
	var n int
	if err := d.Get(&n, `SELECT COUNT(*) FROM devices WHERE status = 'online'`); err != nil {
		t.Fatalf("count online: %v", err)
	}
	return n
}

// TestFlush_RequeuesBatchWhenWriteFails is the regression test for silent batch
// loss. Before the fix, a failed Flush logged and returned, and the pending map
// had already been replaced with an empty one -- so the timestamps were gone and
// the devices stayed offline forever with nothing in the log saying why.
func TestFlush_RequeuesBatchWhenWriteFails(t *testing.T) {
	f := &HeartbeatFlusher{db: closedDB(t), pending: make(map[string]time.Time)}

	f.Record("device-a")
	f.Record("device-b")

	f.Flush()

	if got := f.PendingCount(); got != 2 {
		t.Fatalf("pending after failed flush = %d, want 2 (batch was dropped)", got)
	}
}

// TestFlush_RetriesDrainTheSameBatch proves requeue is a queue, not a leak:
// repeated failures keep exactly one entry per device, and a later successful
// flush against a healthy database writes every one of them.
func TestFlush_RetriesDrainTheSameBatch(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	seedDevice(t, d, "device-c")

	// A flusher pointed at a database that will not accept the write, so every
	// attempt requeues, then pointed at the real one to drain the backlog.
	f := &HeartbeatFlusher{db: closedDB(t), pending: make(map[string]time.Time)}
	f.Record("device-c")
	f.Flush()
	f.Flush()
	f.Flush()

	if got := f.PendingCount(); got != 1 {
		t.Fatalf("pending after 3 failed flushes = %d, want 1 (one entry per device)", got)
	}

	f.db = d
	f.Flush()

	if got := f.PendingCount(); got != 0 {
		t.Errorf("pending after successful flush = %d, want 0", got)
	}
	if got := onlineCount(t, d); got != 1 {
		t.Errorf("online devices = %d, want 1", got)
	}
}

// TestFlush_ConcurrentFlushesDoNotDropDevices guards the flushMu addition: the
// batch swap releases mu for the whole write, so without a separate lock two
// flushes overlap and the loser's batch is discarded.
func TestFlush_ConcurrentFlushesDoNotDropDevices(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	const n = 200
	for i := 0; i < n; i++ {
		seedDevice(t, d, deviceID(i))
	}

	f := &HeartbeatFlusher{db: d, pending: make(map[string]time.Time)}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := g; i < n; i += 8 {
				f.Record(deviceID(i))
				f.Flush()
			}
		}(g)
	}
	wg.Wait()
	f.Flush()

	if got := onlineCount(t, d); got != n {
		t.Errorf("online devices = %d, want %d (concurrent flush dropped a batch)", got, n)
	}
	if got := f.PendingCount(); got != 0 {
		t.Errorf("pending = %d, want 0", got)
	}
}

// TestFlush_RequeueKeepsNewerHeartbeat proves requeue does not resurrect a stale
// timestamp over one recorded while the flush was in flight: replaying the old
// value would move last_seen_at backwards.
func TestFlush_RequeueKeepsNewerHeartbeat(t *testing.T) {
	f := &HeartbeatFlusher{db: closedDB(t), pending: make(map[string]time.Time)}

	old := time.Now().UTC().Add(-time.Hour)
	f.RecordWithTime("device-d", old)
	f.Flush() // fails, requeues the old timestamp

	newer := time.Now().UTC()
	f.RecordWithTime("device-d", newer)

	// Simulate a second failure carrying the stale value.
	f.requeue(map[string]time.Time{"device-d": old})

	f.mu.Lock()
	got := f.pending["device-d"]
	f.mu.Unlock()
	if !got.Equal(newer) {
		t.Errorf("pending timestamp = %v, want the newer %v", got, newer)
	}
}

// TestFlush_RequeueSkipsRemovedDevice proves a disconnect (Remove) during a
// failed flush is not undone: the device must not be written back online.
func TestFlush_RequeueSkipsRemovedDevice(t *testing.T) {
	f := &HeartbeatFlusher{db: closedDB(t), pending: make(map[string]time.Time)}

	f.Record("device-e")
	f.Flush() // fails, device-e is back in pending

	f.Remove("device-e")
	if got := f.PendingCount(); got != 0 {
		t.Errorf("pending after Remove = %d, want 0", got)
	}
}

func deviceID(i int) string {
	return "conc-device-" + strconv.Itoa(i)
}

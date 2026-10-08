package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

func seedTestDevice(t *testing.T, d *sqlx.DB, hostname string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := d.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, status,
			device_secret_hash, enrolled_at, created_at, updated_at)
		VALUES (?, ?, 'linux', '12.04', 'online', 'hash', ?, ?, ?)`,
		hostname, hostname, now, now, now); err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

// TestSnapshot_ProducesRestorableDatabase proves the backup is a real,
// consistent database rather than a byte copy that VACUUM INTO would reject.
func TestSnapshot_ProducesRestorableDatabase(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	seedTestDevice(t, d, "BACKUP-DEVICE-01")

	dest := filepath.Join(dir, "snapshots", "snap-1.db")
	if err := Snapshot(d, dest); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("snapshot is empty")
	}

	// The snapshot must be independently openable and contain the data.
	restored, err := Open(dest)
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer restored.Close()

	var count int
	if err := restored.Get(&count, `SELECT COUNT(*) FROM devices WHERE hostname = 'BACKUP-DEVICE-01'`); err != nil {
		t.Fatalf("query restored: %v", err)
	}
	if count != 1 {
		t.Errorf("restored device count = %d, want 1", count)
	}
}

// TestSnapshot_OverwritesExistingTarget guards the timestamped-name collision
// path: VACUUM INTO fails if the destination exists, so Snapshot must clear it.
func TestSnapshot_OverwritesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	seedTestDevice(t, d, "BACKUP-DEVICE-02")

	dest := filepath.Join(dir, "snap.db")
	if err := Snapshot(d, dest); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if err := Snapshot(d, dest); err != nil {
		t.Fatalf("second snapshot over existing target: %v", err)
	}
}

// TestPruneOldest_KeepsNewestN verifies retention prunes the oldest snapshots
// and never touches unrelated files sharing the directory.
func TestPruneOldest_KeepsNewestN(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()

	// Six snapshots, oldest to newest, plus an unrelated file that must survive.
	for i := 0; i < 6; i++ {
		name := filepath.Join(dir, "endpoint-mgmt-2026010"+string(rune('1'+i))+".db")
		if err := os.WriteFile(name, []byte("snap"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		mod := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(name, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}
	keepMe := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(keepMe, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write unrelated: %v", err)
	}

	if err := pruneOldest(dir, "endpoint-mgmt", 3); err != nil {
		t.Fatalf("pruneOldest: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var snaps, others int
	for _, e := range entries {
		if e.Name() == "unrelated.txt" {
			others++
			continue
		}
		snaps++
	}
	if snaps != 3 {
		t.Errorf("kept %d snapshots, want 3", snaps)
	}
	if others != 1 {
		t.Errorf("unrelated file was touched: found %d", others)
	}
}

// TestPruneOldest_NoopUnderRetention ensures a healthy backup set is untouched.
func TestPruneOldest_NoopUnderRetention(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		name := filepath.Join(dir, "endpoint-mgmt-2026010"+string(rune('1'+i))+".db")
		if err := os.WriteFile(name, []byte("snap"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := pruneOldest(dir, "endpoint-mgmt", 10); err != nil {
		t.Fatalf("pruneOldest: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("got %d files, want 2 (retention should not delete)", len(entries))
	}
}

// TestStartBackupJob_StopsOnContextCancel proves the background job is
// cancellable and does not leak a goroutine after server shutdown.
func TestStartBackupJob_StopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	seedTestDevice(t, d, "BACKUP-DEVICE-03")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		StartBackupJob(ctx, d, BackupConfig{
			Dir:      filepath.Join(dir, "backups"),
			Interval: time.Hour,
			Retain:   3,
			Prefix:   "endpoint-mgmt",
		}, nil)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backup job did not stop on context cancel")
	}
}

func TestDefaultBackupConfig(t *testing.T) {
	cfg := DefaultBackupConfig("data/endpoint-mgmt.db")
	if cfg.Dir != filepath.Join("data", "backups") {
		t.Errorf("Dir = %q", cfg.Dir)
	}
	if cfg.Interval != time.Hour {
		t.Errorf("Interval = %v", cfg.Interval)
	}
	if cfg.Retain != 24 {
		t.Errorf("Retain = %d", cfg.Retain)
	}
}

func TestBackupAndRestore_CLIHelpers(t *testing.T) {
	dir := t.TempDir()
	liveDB := filepath.Join(dir, "live.db")
	backupDB := filepath.Join(dir, "backup.db")
	restoredDB := filepath.Join(dir, "restored.db")

	d, err := Open(liveDB)
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	seedTestDevice(t, d, "CLI-DEVICE-01")
	_ = d.Close()

	// 1. Run Backup helper
	if err := Backup(liveDB, backupDB); err != nil {
		t.Fatalf("Backup helper failed: %v", err)
	}

	// 2. Run Restore helper to restoredDB
	if err := Restore(backupDB, restoredDB); err != nil {
		t.Fatalf("Restore helper failed: %v", err)
	}

	// 3. Verify restoredDB content
	checkDB, err := Open(restoredDB)
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer checkDB.Close()

	var count int
	if err := checkDB.Get(&count, `SELECT COUNT(*) FROM devices WHERE hostname = 'CLI-DEVICE-01'`); err != nil {
		t.Fatalf("query restored db: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 device in restored db, got %d", count)
	}
}

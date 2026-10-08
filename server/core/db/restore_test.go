package db

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRestore_RemovesStaleSidecars is the regression test for the silent
// revert. A -wal left behind by a crash is individually valid by SQLite's own
// rules -- frames are checked against the WAL header and salts, never against
// the main file -- so a restore that swaps the main file but keeps the sidecar
// leaves it free to be replayed on top of the restored content.
//
// The sidecars here are genuine, not placeholder files: real frames are
// captured from a live database and put back after a clean close, which is
// exactly the state a power loss or a killed process leaves behind.
func TestRestore_RemovesStaleSidecars(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.db")

	d, err := Open(live)
	if err != nil {
		t.Fatalf("open live: %v", err)
	}
	seedTestDevice(t, d, "OLD-DEVICE-01")
	// Real committed changes sitting in the WAL, not yet checkpointed.
	if _, err := d.Exec("UPDATE devices SET status = 'online' WHERE hostname = 'OLD-DEVICE-01'"); err != nil {
		t.Fatalf("mark online: %v", err)
	}

	walBytes, err := os.ReadFile(live + "-wal")
	if err != nil {
		t.Fatalf("read live -wal: %v", err)
	}
	if len(walBytes) == 0 {
		t.Fatal("expected a non-empty -wal, the test would prove nothing")
	}
	shmBytes, err := os.ReadFile(live + "-shm")
	if err != nil {
		t.Fatalf("read live -shm: %v", err)
	}

	// A backup whose content differs, so a surviving WAL is detectable.
	backupPath := filepath.Join(dir, "backup.db")
	if err := Snapshot(d, backupPath); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Clean close removes the sidecars; put them back to simulate a crash.
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.WriteFile(live+"-wal", walBytes, 0o600); err != nil {
		t.Fatalf("restore -wal: %v", err)
	}
	if err := os.WriteFile(live+"-shm", shmBytes, 0o600); err != nil {
		t.Fatalf("restore -shm: %v", err)
	}

	if err := Restore(backupPath, live); err != nil {
		t.Fatalf("Restore over a crashed database: %v", err)
	}

	for _, s := range []string{live + "-wal", live + "-shm"} {
		if _, err := os.Stat(s); !os.IsNotExist(err) {
			t.Errorf("stale sidecar %s survived the restore (stat err = %v)", filepath.Base(s), err)
		}
	}
	if online := countOnline(t, live); online != 1 {
		t.Errorf("online devices after restore = %d, want 1", online)
	}
}

// TestRestore_LeavesSourceUnmodified proves the backup file is not written to.
// The old implementation opened sourcePath with a normal handle, which runs
// migrations and creates a -wal beside it, so restoring a backup quietly
// changed the one artefact the operator has left.
func TestRestore_LeavesSourceUnmodified(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")

	d, err := Open(src)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	seedTestDevice(t, d, "SRC-DEVICE-01")
	if err := d.Close(); err != nil {
		t.Fatalf("close src: %v", err)
	}

	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read src: %v", err)
	}
	sidecarsBefore, err := filepath.Glob(src + "-*")
	if err != nil {
		t.Fatalf("glob src sidecars: %v", err)
	}

	if err := Restore(src, filepath.Join(dir, "dest.db")); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("re-read src: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("Restore modified the source backup file")
	}
	sidecarsAfter, err := filepath.Glob(src + "-*")
	if err != nil {
		t.Fatalf("re-glob src sidecars: %v", err)
	}
	if len(sidecarsAfter) != len(sidecarsBefore) {
		t.Errorf("Restore created %v beside the source, had %v before", sidecarsAfter, sidecarsBefore)
	}
}

// TestRestore_RejectsCorruptSource proves validation is real: a truncated or
// non-database file must fail loudly rather than replacing a good database.
func TestRestore_RejectsCorruptSource(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.db")
	if err := os.WriteFile(bad, []byte("this is definitely not a sqlite database"), 0o600); err != nil {
		t.Fatalf("write bad db: %v", err)
	}
	dest := filepath.Join(dir, "dest.db")

	if err := Restore(bad, dest); err == nil {
		t.Fatal("Restore accepted a non-database file")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("dest was created from an invalid source")
	}
}

// TestRestore_RejectsTruncatedDatabase covers a file that starts as a real
// database and is cut short, which is the shape a bad copy actually takes.
func TestRestore_RejectsTruncatedDatabase(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	d, err := Open(src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedTestDevice(t, d, "TRUNC-DEVICE-01")
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	full, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(src, full[:len(full)/2], 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	dest := filepath.Join(dir, "dest.db")
	if err := Restore(src, dest); err == nil {
		t.Error("Restore accepted a truncated database")
	}
}

// TestRestore_LeavesNoTempFile proves the temporary copy is cleaned up on both
// the success and the failure path.
func TestRestore_LeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	d, err := Open(src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedTestDevice(t, d, "TMP-DEVICE-01")
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	dest := filepath.Join(dir, "dest.db")
	if err := Restore(src, dest); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	assertNoTempFiles(t, dir)

	bad := filepath.Join(dir, "bad.db")
	if err := os.WriteFile(bad, []byte("not a database"), 0o600); err != nil {
		t.Fatalf("write bad: %v", err)
	}
	if err := Restore(bad, dest); err == nil {
		t.Fatal("expected failure")
	}
	assertNoTempFiles(t, dir)
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if len(e.Name()) > 5 && e.Name()[len(e.Name())-5:] == ".tmp-" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func countOnline(t *testing.T, path string) int {
	t.Helper()
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer d.Close()
	var n int
	if err := d.Get(&n, `SELECT COUNT(*) FROM devices WHERE status = 'online'`); err != nil {
		t.Fatalf("count online: %v", err)
	}
	return n
}

package db

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jmoiron/sqlx"
)

// BackupConfig controls the periodic online backup job.
type BackupConfig struct {
	// Dir is where timestamped snapshots are written.
	Dir string
	// Interval is how often a snapshot is taken.
	Interval time.Duration
	// Retain is how many snapshots to keep; older ones are pruned.
	Retain int
	// Prefix is the snapshot filename prefix, e.g. "endpoint-mgmt".
	Prefix string
}

// DefaultBackupConfig returns a sane production default: hourly snapshots,
// 24 hours of history, written under a "backups" dir beside the database.
func DefaultBackupConfig(dbPath string) BackupConfig {
	return BackupConfig{
		Dir:      filepath.Join(filepath.Dir(dbPath), "backups"),
		Interval: time.Hour,
		Retain:   24,
		Prefix:   "endpoint-mgmt",
	}
}

// Snapshot takes a consistent, hot backup using SQLite's VACUUM INTO.
//
// VACUUM INTO is preferred over copying the .db file because the database runs
// in WAL mode: a plain file copy can capture a torn or stale snapshot, while
// VACUUM INTO takes a read lock and writes a fully consistent, defragmented
// image in a single pass without stopping the server.
func Snapshot(d *sqlx.DB, destPath string) error {
	if dir := filepath.Dir(destPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create backup dir: %w", err)
		}
	}
	// VACUUM INTO refuses to overwrite an existing file, so clear the
	// destination first. Timestamped names make this a no-op in practice.
	if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear backup target: %w", err)
	}
	if _, err := d.Exec(`VACUUM INTO ?`, destPath); err != nil {
		return fmt.Errorf("vacuum into %s: %w", destPath, err)
	}
	return nil
}

// pruneOldest removes snapshots beyond the retention window, oldest first.
// Only files whose name starts with prefix are considered, so unrelated files
// in the same directory are never touched.
func pruneOldest(dir, prefix string, retain int) error {
	if retain <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read backup dir: %w", err)
	}
	var snapshots []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".db" {
			continue
		}
		if prefix != "" && (len(name) < len(prefix) || name[:len(prefix)] != prefix) {
			continue
		}
		snapshots = append(snapshots, filepath.Join(dir, name))
	}
	if len(snapshots) <= retain {
		return nil
	}
	// Sort newest-first, then drop everything past the retention window.
	sort.Slice(snapshots, func(i, j int) bool {
		ai, aerr := os.Stat(snapshots[i])
		bi, berr := os.Stat(snapshots[j])
		if aerr != nil || berr != nil {
			return snapshots[i] > snapshots[j]
		}
		return ai.ModTime().After(bi.ModTime())
	})
	for _, path := range snapshots[retain:] {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("prune %s: %w", path, err)
		}
	}
	return nil
}

// StartBackupJob runs a periodic online backup until ctx is cancelled. Errors
// are reported through onError; a failed backup must never take the server
// down, so nothing here returns or panics.
func StartBackupJob(ctx context.Context, d *sqlx.DB, cfg BackupConfig, onError func(error)) {
	if cfg.Dir == "" {
		return
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "endpoint-mgmt"
	}
	if onError == nil {
		onError = func(error) {}
	}

	run := func() {
		name := fmt.Sprintf("%s-%s.db", cfg.Prefix, time.Now().UTC().Format("20060102T150405Z"))
		if err := Snapshot(d, filepath.Join(cfg.Dir, name)); err != nil {
			onError(err)
			return
		}
		if err := pruneOldest(cfg.Dir, cfg.Prefix, cfg.Retain); err != nil {
			onError(err)
		}
	}

	// Short initial delay so a freshly migrated database settles and startup
	// latency is not paid by the first incoming request.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
		run()
	}

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// Backup takes an online hot snapshot of the database at dbPath and saves it to destPath.
func Backup(dbPath, destPath string) error {
	d, err := Open(dbPath)
	if err != nil {
		return fmt.Errorf("open source db for backup: %w", err)
	}
	defer d.Close()

	if err := Snapshot(d, destPath); err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}
	return nil
}

// Restore validates the SQLite database at sourcePath and atomically replaces destPath.
//
// Validation runs against a throwaway copy, never against sourcePath. Opening
// the backup with a normal handle makes SQLite run migrations against it and
// write a -wal beside it, so a read-only artefact of a restore silently becomes
// a modified one; a temp copy has none of those consequences for the operator's
// only remaining copy of their data.
func Restore(sourcePath, destPath string) error {
	if dir := filepath.Dir(destPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create dest dir %s: %w", dir, err)
		}
	}

	tmpPath := destPath + fmt.Sprintf(".tmp-%d", time.Now().UnixNano())
	cleanup := func() { _ = os.Remove(tmpPath) }

	if err := copyFile(sourcePath, tmpPath); err != nil {
		cleanup()
		return err
	}
	if err := quickCheck(tmpPath); err != nil {
		cleanup()
		return err
	}

	// The sidecars belong to the database being replaced, not to the one being
	// installed. Leaving them behind is the worst case in this function: SQLite
	// validates a WAL frame against that WAL's own header and salts, never
	// against the main file, so a stale -wal replays as valid and silently
	// overwrites the restored content with the previous database's pages. It
	// only bites when the last connection did not close cleanly, which is
	// exactly the moment a restore is most likely to be run.
	for _, sidecar := range []string{destPath + "-wal", destPath + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			cleanup()
			return fmt.Errorf("remove stale %s: %w", filepath.Base(sidecar), err)
		}
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		cleanup()
		// On Windows this is overwhelmingly a running server, not a broken file:
		// neither Go's syscall.Open nor SQLite's winOpen pass FILE_SHARE_DELETE,
		// so an open database cannot be replaced. The bare errno reads like data
		// loss, so name the action that actually fixes it.
		return fmt.Errorf("replace dest db %s: %w\n"+
			"  if the server is running, stop it first (an open database cannot be replaced)", destPath, err)
	}
	return nil
}

// quickCheck reports whether the SQLite database at path is structurally sound.
func quickCheck(path string) error {
	d, err := sqlx.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open restore candidate: %w", err)
	}
	defer d.Close()

	var result string
	if err := d.Get(&result, "PRAGMA quick_check"); err != nil {
		return fmt.Errorf("validate %s: %w", filepath.Base(path), err)
	}
	if result != "ok" {
		return fmt.Errorf("source database %s is corrupt: %s", filepath.Base(path), result)
	}
	return nil
}

// copyFile streams src to dst. Databases are copied in chunks rather than with
// os.ReadFile because a restore should not need the whole file resident in RAM
// to move it, and a production console database is measured in gigabytes.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("read source file: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write temp restore file: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy source file: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close temp restore file: %w", err)
	}
	return nil
}

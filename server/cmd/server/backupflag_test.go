package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDBPathForBackupReadsEnvFile is the regression test for the restore
// target bug. -backup and -restore used to call config.Load() and return before
// -env-file was read, so on a service host -- where the whole point is that the
// SCM gives the process no environment -- DB_PATH resolved to the relative
// default. A restore then created a fresh database at data/endpoint-mgmt.db
// beside the real one and printed "restored successfully".
func TestDBPathForBackupReadsEnvFile(t *testing.T) {
	// loadEnvFile treats a key that merely exists as already configured
	// (main.go, the os.LookupEnv check), so the key has to be genuinely absent
	// rather than set to the empty string -- t.Setenv cannot express that.
	unsetForEnvFileTest(t, "DB_PATH")
	dir := t.TempDir()
	envPath := filepath.Join(dir, "server.env")
	want := filepath.Join(dir, "real-server.db")
	if err := os.WriteFile(envPath, []byte("DB_PATH="+want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadEnvFile(envPath); err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}

	if got := dbPathForBackup(); got != want {
		t.Errorf("database path = %q, want the one from the env file %q", got, want)
	}
}

// unsetForEnvFileTest removes a key for the duration of the test, restoring the
// previous value afterwards so it cannot leak into a sibling test.
func unsetForEnvFileTest(t *testing.T, key string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(key, prev)
			return
		}
		os.Unsetenv(key)
	})
}

// TestDBPathForBackupNeedsNoJWTSecret proves the recovery path is not gated on
// the configuration that most often stops a server from starting. config.Load
// rejects an empty JWT_SECRET, so routing -restore through it made a restore
// impossible on a server whose secret is the thing that is broken.
func TestDBPathForBackupNeedsNoJWTSecret(t *testing.T) {
	want := filepath.Join(t.TempDir(), "no-secret.db")
	t.Setenv("DB_PATH", want)
	t.Setenv("JWT_SECRET", "")

	if got := dbPathForBackup(); got != want {
		t.Errorf("database path = %q, want %q with no JWT_SECRET set", got, want)
	}
}

// TestDBPathForBackupDefault keeps the standalone default honest: with nothing
// configured at all, the path is the relative one the whole single-binary
// deployment has always used.
func TestDBPathForBackupDefault(t *testing.T) {
	t.Setenv("DB_PATH", "")
	if got := dbPathForBackup(); got != "data/endpoint-mgmt.db" {
		t.Errorf("default database path = %q, want data/endpoint-mgmt.db", got)
	}
}

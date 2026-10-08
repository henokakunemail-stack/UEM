package main

import (
	"path/filepath"
	"testing"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
)

// TestOpenDatabase_DefaultsToSQLite keeps the standalone single-binary mode
// exactly as it was: no database configuration at all still opens a local
// SQLite file.
func TestOpenDatabase_DefaultsToSQLite(t *testing.T) {
	d, err := openDatabase(config.Config{DBPath: filepath.Join(t.TempDir(), "x.db")})
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	defer d.Close()
	if got := d.DriverName(); got != "sqlite" {
		t.Errorf("driver = %q, want sqlite", got)
	}
}

// TestOpenDatabase_SQLiteDriverIsExplicit is the near-miss that matters: an
// operator who sets DB_DRIVER=postgres but forgets the URL must get an error
// naming the missing setting, not an empty SQLite database that looks like a
// successful start with no fleet in it.
func TestOpenDatabase_SQLiteDriverIsExplicit(t *testing.T) {
	_, err := openDatabase(config.Config{
		DBDriver: "postgres",
		DBPath:   filepath.Join(t.TempDir(), "x.db"),
	})
	if err == nil {
		t.Fatal("expected an error for DB_DRIVER=postgres with no DB_URL")
	}
}

// TestOpenDatabase_IgnoresBareSQLiteDriver proves the explicit-but-default case
// is still SQLite rather than an error, since DB_DRIVER=sqlite is a legitimate
// way to say "the default".
func TestOpenDatabase_IgnoresBareSQLiteDriver(t *testing.T) {
	d, err := openDatabase(config.Config{
		DBDriver: "sqlite",
		DBPath:   filepath.Join(t.TempDir(), "x.db"),
	})
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	defer d.Close()
	if got := d.DriverName(); got != "sqlite" {
		t.Errorf("driver = %q, want sqlite", got)
	}
}

// TestConfig_ReadsPostgresSettings proves the two settings reach the config at
// all -- the previous commit added a PostgreSQL branch that no configuration
// could ever select, which is what made it dead code.
func TestConfig_ReadsPostgresSettings(t *testing.T) {
	t.Setenv("DB_DRIVER", "postgres")
	t.Setenv("DB_URL", "postgres://u:p@localhost:5432/uem?sslmode=disable")
	t.Setenv("JWT_SECRET", "test-secret-value")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DBDriver != "postgres" {
		t.Errorf("DBDriver = %q, want postgres", cfg.DBDriver)
	}
	if cfg.DBURL == "" {
		t.Error("DBURL is empty, so the driver could never be selected")
	}
}

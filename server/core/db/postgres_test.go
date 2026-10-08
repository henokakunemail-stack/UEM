package db

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

// postgresURL returns the connection string for the live PostgreSQL used by the
// smoke tests, or "" when there is none configured.
//
// The credential comes from the environment and is never written down, and the
// whole file skips without it. A test that needs a secret must not be a condition
// for a green build: CI has no PostgreSQL, and a test that fails there teaches
// everyone to ignore it.
func postgresURL() string {
	url := os.Getenv("UEM_TEST_POSTGRES_URL")
	if url == "" {
		return ""
	}
	return url
}

// openPostgresForTest opens a scratch database that is dropped when the test
// finishes. Each test gets its own, so one that leaves a stray table behind
// cannot make the next one lie.
func openPostgresForTest(t *testing.T) *sqlx.DB {
	t.Helper()
	url := postgresURL()
	if url == "" {
		t.Skip("UEM_TEST_POSTGRES_URL not set")
	}

	admin, err := sqlx.Connect("postgres", url)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	defer admin.Close()

	// A generated name, so a rerun after a crash never collides with the
	// half-finished database the previous run left behind.
	name := fmt.Sprintf("uem_probe_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() {
		cleanup, err := sqlx.Connect("postgres", url)
		if err != nil {
			t.Logf("cleanup connect: %v", err)
			return
		}
		defer cleanup.Close()
		// The connections this test opened have to be gone first, or DROP
		// DATABASE fails on a database that is still in use.
		if _, err := cleanup.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
			t.Logf("cleanup drop %s: %v", name, err)
		}
	})

	d, err := OpenWithDriver("postgres", dbURLForTest(url, name))
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// dbURLForTest points a connection string at a different database on the same
// server, preserving credentials and options.
func dbURLForTest(baseURL, name string) string {
	if i := strings.Index(baseURL, "?"); i >= 0 {
		return baseURL[:strings.LastIndex(baseURL[:i], "/")+1] + name + baseURL[i:]
	}
	if i := strings.LastIndex(baseURL, "/"); i >= 0 {
		return baseURL[:i+1] + name
	}
	return baseURL
}

// TestPostgres_MigrationsApplyAndAreRecorded is the claim this whole dialect
// layer exists to support: that the migration set runs against PostgreSQL and
// records every version. Before the dialect work it failed on the first
// statement with `type "datetime" does not exist`, which reads like a
// connectivity problem rather than a syntax one.
func TestPostgres_MigrationsApplyAndAreRecorded(t *testing.T) {
	d := openPostgresForTest(t)

	// Open already ran the migrations. Count the versions it recorded.
	var applied int
	if err := d.Get(&applied, `SELECT COUNT(*) FROM schema_migrations`); err != nil {
		t.Fatalf("count applied: %v", err)
	}
	if want := len(migrationNames(t)); applied != want {
		t.Errorf("recorded %d migrations, want %d", applied, want)
	}

	// The columns the first migration creates must be real PostgreSQL columns of
	// the real type, not just rows in the bookkeeping table.
	for _, col := range []struct{ table, column, dataType string }{
		{"devices", "enrolled_at", "timestamp with time zone"},
		{"devices", "hostname", "text"},
		{"schema_migrations", "applied_at", "timestamp with time zone"},
	} {
		var got string
		err := d.Get(&got, `SELECT data_type FROM information_schema.columns
			WHERE table_name = $1 AND column_name = $2`, col.table, col.column)
		if err != nil {
			t.Errorf("look up %s.%s: %v", col.table, col.column, err)
			continue
		}
		if got != col.dataType {
			t.Errorf("%s.%s is %q, want %q", col.table, col.column, got, col.dataType)
		}
	}
}

// TestPostgres_MigrationsAreIdempotent re-opens the same database, which is the
// restart path. Every version is already recorded, so nothing may run again.
func TestPostgres_MigrationsAreIdempotent(t *testing.T) {
	d := openPostgresForTest(t)
	if err := MigrateWithDialect(d, dialectFor("postgres")); err != nil {
		t.Fatalf("re-running migrations on a current database: %v", err)
	}
	var applied int
	if err := d.Get(&applied, `SELECT COUNT(*) FROM schema_migrations`); err != nil {
		t.Fatalf("count applied: %v", err)
	}
	if want := len(migrationNames(t)); applied != want {
		t.Errorf("recorded %d migrations after a re-run, want %d", applied, want)
	}
}

// TestPostgres_CrudRoundTrip proves the parts the migrations cannot prove on
// their own: that a timestamp written through Go reads back as a time.Time, and
// that a bound parameter actually binds. lib/pq returns timestamptz as a
// time.Time, so the 139 time.Time struct fields work once the column type is
// right -- which the migration test above is what establishes.
//
// Every query here goes through db.Rebind, and that is the shape application
// code has to use. sqlx does not rewrite placeholders on its own: Rebind is an
// explicit call, and a raw '?' reaching PostgreSQL is a syntax error rather
// than a silent no-op, so the failure is loud and immediate.
//
// ponytail: the server's own 83 call sites still pass '?' straight to the
// driver, so PostgreSQL supports the schema and the migration path today, not
// the full request path. Threading Rebind through every repository is the
// remaining work; the SQLite path is unaffected because Rebind on a QUESTION
// bind type is the identity.
func TestPostgres_CrudRoundTrip(t *testing.T) {
	d := openPostgresForTest(t)

	seedPostgresDevice(t, d, "PG-DEVICE-01")
	seedPostgresDevice(t, d, "PG-DEVICE-02")

	var row struct {
		Hostname  string    `db:"hostname"`
		EnrolledA time.Time `db:"enrolled_at"`
	}
	q := d.Rebind(`SELECT hostname, enrolled_at FROM devices WHERE id = ?`)
	if err := d.Get(&row, q, "PG-DEVICE-01"); err != nil {
		t.Fatalf("read back device: %v", err)
	}
	if row.Hostname != "PG-DEVICE-01" {
		t.Errorf("hostname = %q, want PG-DEVICE-01", row.Hostname)
	}
	if row.EnrolledA.IsZero() {
		t.Error("enrolled_at came back as the zero time, so the bind or the type is wrong")
	}

	// The IN (?, ?) form is the one place a placeholder inside a parenthesised
	// list tends to break a naive rebind, and it is used throughout the server.
	var n int
	q = d.Rebind(`SELECT COUNT(*) FROM devices WHERE id IN (?, ?)`)
	if err := d.Get(&n, q, "PG-DEVICE-01", "PG-DEVICE-02"); err != nil {
		t.Fatalf("IN-list query: %v", err)
	}
	if n != 2 {
		t.Errorf("matched %d devices, want 2", n)
	}
}

// seedPostgresDevice is seedTestDevice with a rebound query: the shared helper
// passes '?' raw, which PostgreSQL rejects.
func seedPostgresDevice(t *testing.T, d *sqlx.DB, hostname string) {
	t.Helper()
	now := time.Now().UTC()
	q := d.Rebind(`
		INSERT INTO devices (id, hostname, os_name, os_version, status,
			device_secret_hash, enrolled_at, created_at, updated_at)
		VALUES (?, ?, 'linux', '12.04', 'online', 'hash', ?, ?, ?)`)
	if _, err := d.Exec(q, hostname, hostname, now, now, now); err != nil {
		t.Fatalf("seed %s: %v", hostname, err)
	}
}

// TestPostgres_BuiltinScriptTemplatesSeed proves the INSERT OR IGNORE
// translation against the real statements rather than a synthetic one: 0018
// installs nine fixed templates, and `INSERT OR IGNORE` is a syntax error there.
func TestPostgres_BuiltinScriptTemplatesSeed(t *testing.T) {
	d := openPostgresForTest(t)

	var seeded int
	if err := d.Get(&seeded, `SELECT COUNT(*) FROM script_templates`); err != nil {
		t.Fatalf("count seeded templates: %v", err)
	}
	if seeded == 0 {
		t.Fatal("no script templates were seeded")
	}

	// Re-running the seeding must be a no-op rather than a duplicate-key error.
	if err := MigrateWithDialect(d, dialectFor("postgres")); err != nil {
		t.Fatalf("re-running migrations: %v", err)
	}
	var after int
	if err := d.Get(&after, `SELECT COUNT(*) FROM script_templates`); err != nil {
		t.Fatalf("recount: %v", err)
	}
	if after != seeded {
		t.Errorf("template count changed from %d to %d on re-run", seeded, after)
	}
}

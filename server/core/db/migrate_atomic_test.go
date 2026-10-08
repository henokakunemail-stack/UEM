package db

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jmoiron/sqlx"
)

// A database that has already been migrated has to survive the exact sequence
// that used to make it unstartable: the bookkeeping lost, the migration run
// again over a schema that already has the change.
//
// This is the production shape. An older runner applied a script and then lost
// the version row, so the next startup began from 0001 again -- and 0002's bare
// ALTER TABLE hit a retired_at column that was already there. Every restart
// reached the same statement and died there.
func TestAMigratedDatabaseSurvivesItsOwnBookkeepingBeingLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var recorded int
	if err := d.Get(&recorded, `SELECT COUNT(*) FROM schema_migrations`); err != nil {
		t.Fatalf("count applied: %v", err)
	}
	if recorded == 0 {
		t.Fatal("Open applied no migrations")
	}
	t.Logf("%d migrations applied and recorded by Open", recorded)

	// The lost-bookkeeping state, reached directly rather than by racing a
	// failure at the right instant.
	if _, err := d.Exec(`DELETE FROM schema_migrations`); err != nil {
		t.Fatalf("clear bookkeeping: %v", err)
	}

	if err := Migrate(d); err != nil {
		t.Fatalf("Migrate over an already-migrated schema failed: %v", err)
	}

	// Not just "no error": every version recorded again, so the next restart is
	// a no-op rather than another full replay.
	var after int
	if err := d.Get(&after, `SELECT COUNT(*) FROM schema_migrations`); err != nil {
		t.Fatalf("count reapplied: %v", err)
	}
	if after != recorded {
		t.Fatalf("reapplied %d migrations, want %d; a version is still being lost between applying and recording it", after, recorded)
	}
	if err := d.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

// The filter that makes the test above pass has to be driven by the real schema,
// not by reading the script text, and it has to leave a column that is missing
// alone. Both halves matter: drop every ADD COLUMN unconditionally and the four
// widening migrations silently stop adding their columns.
func TestAColumnIsSkippedOnlyWhenTheSchemaAlreadyHasIt(t *testing.T) {
	d := openMigrated(t)

	stmt, err := fs.ReadFile(migrations, "migrations/0002_device_mgmt.sql")
	if err != nil {
		t.Fatalf("read 0002: %v", err)
	}

	tx, err := d.Beginx()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	body, err := skipPresentColumns(tx, dialectFor("sqlite"), string(stmt))
	if err != nil {
		t.Fatalf("skipPresentColumns: %v", err)
	}

	if strings.Contains(body, "ADD COLUMN retired_at") {
		t.Error("kept ADD COLUMN retired_at, which 0002 has already applied to this database")
	}
	if strings.Contains(body, "ADD COLUMN capabilities") {
		t.Error("kept ADD COLUMN capabilities, which 0002 has already applied to this database")
	}
	// The tables 0002 creates must survive the rewrite; only the ADD COLUMN
	// lines are dropped.
	if !strings.Contains(body, "CREATE TABLE IF NOT EXISTS device_groups") {
		t.Error("dropped a CREATE TABLE that has nothing to do with ADD COLUMN")
	}

	// A column that is not there yet has to be left in place.
	missing := `ALTER TABLE devices ADD COLUMN probe_absent TEXT;`
	kept, err := skipPresentColumns(tx, dialectFor("sqlite"), missing)
	if err != nil {
		t.Fatalf("skipPresentColumns on a missing column: %v", err)
	}
	if kept != missing {
		t.Errorf("dropped ADD COLUMN for a column that does not exist: %q", kept)
	}

	// A statement the pattern does not match passes through byte for byte, so an
	// unrecognised form fails exactly as it did before rather than vanishing.
	unrecognised := `ALTER TABLE devices RENAME COLUMN retired_at TO retired_on;`
	passthrough, err := skipPresentColumns(tx, dialectFor("sqlite"), unrecognised)
	if err != nil {
		t.Fatalf("skipPresentColumns on an unrecognised statement: %v", err)
	}
	if passthrough != unrecognised {
		t.Errorf("rewrote a statement it does not understand: %q", passthrough)
	}
}

// A migration that fails has to leave nothing behind: no partial schema change
// and no version row. Before the transaction, a script that errored after its
// first statement left that statement's effect committed with nothing recording
// it, and the next startup replayed from the top.
//
// The fixture is injected rather than added to the real migration set, because a
// deliberately broken script in migrations/ would be applied to every database in
// every environment.
func TestAFailedMigrationCommitsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	before, err := columnNames(d, "devices")
	if err != nil {
		t.Fatalf("read columns before: %v", err)
	}
	var recordedBefore int
	if err := d.Get(&recordedBefore, `SELECT COUNT(*) FROM schema_migrations`); err != nil {
		t.Fatalf("count before: %v", err)
	}

	// A script whose first statement succeeds and whose second cannot: the table
	// is created, then a column is added to a table that was never created.
	restore := swapMigrations(t, map[string]string{
		"9000_probe.sql": `
			CREATE TABLE migrate_probe (id TEXT PRIMARY KEY);
			ALTER TABLE migrate_never_created ADD COLUMN oops TEXT;
		`,
	})
	defer restore()

	err = Migrate(d)
	if err == nil {
		t.Fatal("a migration that cannot apply succeeded")
	}
	t.Logf("apply failed as intended: %v", err)

	// The table the first statement created must not exist: the transaction was
	// abandoned, not committed.
	var probe int
	if err := d.Get(&probe, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'migrate_probe'`); err != nil {
		t.Fatalf("look for migrate_probe: %v", err)
	}
	if probe != 0 {
		t.Error("migrate_probe survived a failed migration; the script was applied outside the transaction")
	}

	// No version row for it either.
	var version int
	if err := d.Get(&version, `SELECT COUNT(*) FROM schema_migrations WHERE version = '9000_probe.sql'`); err != nil {
		t.Fatalf("look for the version row: %v", err)
	}
	if version != 0 {
		t.Error("recorded a version for a migration that never applied")
	}

	after, err := columnNames(d, "devices")
	if err != nil {
		t.Fatalf("read columns after: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("devices columns went from %d to %d", len(before), len(after))
	}
	var recordedAfter int
	if err := d.Get(&recordedAfter, `SELECT COUNT(*) FROM schema_migrations`); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if recordedAfter != recordedBefore {
		t.Errorf("schema_migrations went from %d rows to %d", recordedBefore, recordedAfter)
	}
}

// A failed migration must not poison the next attempt either: the operator fixes
// the script and restarts. Here the fixture is replaced with one that succeeds,
// and the same database has to accept it.
func TestAFixedMigrationAppliesOnTheNextRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	restore := swapMigrations(t, map[string]string{
		"9000_probe.sql": `
			CREATE TABLE migrate_probe (id TEXT PRIMARY KEY);
			ALTER TABLE migrate_never_created ADD COLUMN oops TEXT;
		`,
	})
	err = Migrate(d)
	restore()
	if err == nil {
		t.Fatal("the broken fixture applied; the failure path is untested")
	}
	t.Logf("first run failed: %v", err)

	// The operator corrects the script and restarts.
	restore = swapMigrations(t, map[string]string{
		"9000_probe.sql": `
			CREATE TABLE migrate_probe (id TEXT PRIMARY KEY);
			ALTER TABLE devices ADD COLUMN probe_applied TEXT;
		`,
	})
	defer restore()

	if err := Migrate(d); err != nil {
		t.Fatalf("the corrected migration did not apply on the second run: %v", err)
	}
	cols, err := columnNames(d, "migrate_probe")
	if err != nil {
		t.Fatalf("read migrate_probe columns: %v", err)
	}
	if len(cols) == 0 {
		t.Error("migrate_probe does not exist after the corrected migration applied")
	}
	devices, err := columnNames(d, "devices")
	if err != nil {
		t.Fatalf("read devices columns: %v", err)
	}
	if !contains(devices, "probe_applied") {
		t.Error("the corrected migration's column is missing")
	}
	t.Logf("the corrected migration applied on the second run")
}

// SQLite has no ADD COLUMN IF NOT EXISTS, so skipPresentColumns is the only thing
// standing between an already-migrated database and a server that cannot start.
// If a future SQLite gains the statement, this test says the filter can go.
//
// A widening migration that is not named here is itself the failure: the list is
// what tells the next reader that skipPresentColumns is load-bearing for that
// file. 0020 widens devices twice and was never added, so the list silently went
// stale. The second half of the test walks the migrations directory and fails on
// any bare ALTER TABLE the list does not carry, which is what keeps that from
// happening again -- a migration landing outside the list now has to be noticed.
func TestTheMigrationsThatWidenATableAreNamedHere(t *testing.T) {
	widening := map[string]bool{
		"0002_device_mgmt.sql":                      true,
		"0007_user_management.sql":                  true,
		"0015_software_uninstall.sql":               true,
		"0016_auth_sessions_and_audit_chain.sql":    true,
		"0020_patch_scan_and_enrollment_expiry.sql": true,
		"0021_agent_update_signing.sql":             true,
	}
	for name := range widening {
		stmt, err := fs.ReadFile(migrations, "migrations/"+name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(stmt), "ALTER TABLE") {
			t.Errorf("%s no longer has a bare ALTER TABLE; if it now uses ADD COLUMN IF NOT EXISTS, delete skipPresentColumns and this test", name)
		}
	}

	// Everything else in the directory that widens a table must be in the map
	// above. `ADD COLUMN` inside a bare ALTER TABLE is the statement the filter
	// exists for; a CREATE TABLE or an index does not need it.
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if widening[e.Name()] {
			continue
		}
		stmt, err := fs.ReadFile(migrations, "migrations/"+e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if strings.Contains(string(stmt), "ALTER TABLE") {
			t.Errorf("%s widens a table but is not in the list above; "+
				"skipPresentColumns is load-bearing for it and nothing says so",
				e.Name())
		}
	}
}

// swapMigrations points Migrate at an extra fixture script and returns the
// function that puts the real set back.
func swapMigrations(t *testing.T, extra map[string]string) func() {
	t.Helper()
	saved := migrations
	migrations = overlay(saved, extra)
	return func() { migrations = saved }
}

// overlayFS adds files to the "migrations" directory of an existing filesystem.
//
// Both Open and ReadDir are needed: Migrate lists the directory before it reads
// anything, so an overlay that only serves Open hides its own files and the
// fixture never runs.
type overlayFS struct {
	base  fs.FS
	extra fstest.MapFS
}

func (o *overlayFS) Open(name string) (fs.File, error) {
	if base, ok := strings.CutPrefix(name, "migrations/"); ok {
		if _, ok := o.extra[base]; ok {
			return o.extra.Open(base)
		}
	}
	return o.base.Open(name)
}

func (o *overlayFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(o.base, name)
	if err != nil || name != "migrations" {
		return entries, err
	}
	extra, err := o.extra.ReadDir(".")
	if err != nil {
		return nil, err
	}
	// Fixture names are numbered above the real set, so appending cannot
	// collide with a script that is already there.
	return append(entries, extra...), nil
}

func overlay(base fs.FS, extra map[string]string) fs.FS {
	m := make(fstest.MapFS, len(extra))
	for name, content := range extra {
		m[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return &overlayFS{base: base, extra: m}
}

func columnNames(d *sqlx.DB, table string) ([]string, error) {
	cols := []string{}
	if err := d.Select(&cols, `SELECT name FROM pragma_table_info(?)`, table); err != nil {
		return nil, err
	}
	return cols, nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func openMigrated(t *testing.T) *sqlx.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

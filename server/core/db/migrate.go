package db

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrations is the filesystem Migrate reads from, narrowed to fs.FS so a test
// can substitute a fixture containing a migration that fails. Testing atomicity
// otherwise needs a broken script in the real set, which would then be applied
// to every database in every environment.
var migrations fs.FS = migrationsFS

// Migrate applies all embedded SQL migrations in order, recording applied versions
// in schema_migrations so each script is executed exactly once.
func Migrate(d *sqlx.DB) error {
	return MigrateWithDialect(d, dialectFor(d.DriverName()))
}

// MigrateWithDialect applies the migrations using an explicit dialect. Migrate
// infers it from the driver's own name; this form exists so a caller that
// registered the same database under a name the inference does not recognise
// can still be migrated.
func MigrateWithDialect(d *sqlx.DB, dia dialect) error {
	if _, err := d.Exec(dia.schemaMigrationsDDL()); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	files := make([]string, 0, len(names))
	for _, f := range names {
		if strings.HasSuffix(f.Name(), ".sql") {
			files = append(files, f.Name())
		}
	}
	sort.Strings(files)

	// Snapshot before the first script is applied, but only when there is
	// something to apply. The periodic backup job starts after Open returns, so
	// without this the first migration of a release runs against a database that
	// has no restore point at all -- and that is the one migration whose damage
	// a transactional apply cannot undo, because a script that succeeds when it
	// should not have is committed by definition.
	if err := snapshotBeforeMigrate(d, dia, files); err != nil {
		return err
	}

	// Each migration is applied in its own transaction together with the row that
	// records it. SQLite has transactional DDL, so the schema change and the
	// bookkeeping either both land or neither does.
	//
	// That matters because the two were separate statements before. A failure
	// between them left the schema changed with no record of it, and the next
	// startup started from the top again -- landing on the first migration that
	// is not safe to apply twice and failing there forever, with no way back.
	//
	// One transaction per migration rather than one for the whole set: a
	// migration that fails should not undo the ones before it, so the operator
	// fixes that one script and restarts.
	for _, name := range files {
		stmt, err := fs.ReadFile(migrations, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		// BEGIN takes the write lock immediately (see _txlock in the DSN), so the
		// lookup below is not a read that a concurrent second server can slip a
		// migration in between.
		tx, err := d.Beginx()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}

		// Inside the transaction, so the answer and the write it guards are the
		// same unit. The error is returned rather than discarded: a lookup that
		// fails tells us nothing about whether the migration ran, and reading it
		// as "not applied" is how an already-applied script gets applied twice.
		var exists int
		if err := tx.Get(&exists, dia.q(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`), name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists > 0 {
			_ = tx.Rollback()
			continue
		}

		// An ALTER TABLE ... ADD COLUMN is the one statement in these scripts
		// that is not idempotent: SQLite has no ADD COLUMN IF NOT EXISTS, so a
		// column that is already there fails the whole migration. A database can
		// reach that state without the runner ever creating it -- an older runner
		// applied a script and then lost the version row, leaving the column
		// behind with nothing recording that the script had run -- and it is the
		// state that used to make the server unstartable. Dropping the statement
		// whose intent is already satisfied is what lets such a database migrate.
		body, err := skipPresentColumns(tx, dia, string(stmt))
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("check migration %s: %w", name, err)
		}

		if _, err := tx.Exec(dia.translate(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(dia.q(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`),
			name, time.Now().UTC()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}

// snapshotBeforeMigrate writes a restore point before the first script of a
// release is applied.
//
// Transactional DDL already protects the common failure -- a script that errors
// partway through rolls back whole. What a transaction cannot protect is a script
// that succeeds when it should not have, and that is exactly the upgrade case:
// the periodic backup job only starts once Open returns (db.go:126), so without
// this the first migration of a release lands on a database whose newest restore
// point is from last hour.
//
// Skipped when there is nothing to protect: a database where every script is
// already recorded is a restart, and one where none is recorded is new. Only the
// middle -- applied migrations plus pending ones -- is an upgrade.
//
// Skipped entirely on PostgreSQL, and that is a real gap rather than an
// oversight. SQLite's snapshot is VACUUM INTO, which needs a file path it can
// derive from pragma_database_list; PostgreSQL's equivalent needs a server-side
// destination it does not know, because the database has no local file to name.
// Wiring that up means a pg_dump invocation or a server-side directory, which is
// a deployment decision rather than a code detail. The operator who chose
// PostgreSQL is expected to have scheduled their own backups, and an upgrade
// that fails is recoverable in a way a silently corrupted upgrade is not.
func snapshotBeforeMigrate(d *sqlx.DB, dia dialect, files []string) error {
	if dia.postgres {
		return nil
	}
	var applied int
	if err := d.Get(&applied, `SELECT COUNT(*) FROM schema_migrations`); err != nil {
		return fmt.Errorf("count applied migrations: %w", err)
	}
	if applied == 0 || applied >= len(files) {
		return nil
	}

	// pragma_database_list is how the open file is named from inside the handle.
	// Migrate is given a pool rather than a path, and threading the path through
	// every caller of Open for one snapshot is more plumbing than this is worth.
	var dbPath string
	if err := d.Get(&dbPath, `SELECT file FROM pragma_database_list WHERE name = 'main'`); err != nil {
		return fmt.Errorf("resolve database path: %w", err)
	}
	if dbPath == "" {
		return nil
	}

	dir := filepath.Join(filepath.Dir(dbPath), "backups")
	name := fmt.Sprintf("pre-migrate-%s.db", time.Now().UTC().Format("20060102T150405Z"))
	if err := Snapshot(d, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("snapshot before migrating: %w", err)
	}
	return nil
}

// addColumnRe matches a single-statement ALTER TABLE ... ADD COLUMN line and
// captures the table and column names. Anchored at the start of the line so a
// multi-line CREATE TABLE body is never mistaken for one.
var addColumnRe = regexp.MustCompile(`(?i)^\s*ALTER\s+TABLE\s+(\w+)\s+ADD\s+COLUMN\s+(\w+)`)

// skipPresentColumns removes the ADD COLUMN statements a migration would repeat.
//
// SQLite has no ADD COLUMN IF NOT EXISTS, so the four scripts that widen an
// existing table (0002, 0007, 0015, 0016) fail outright when the column is
// already there. That state is reachable on its own: a database whose bookkeeping
// was lost, or one that was patched by hand, and the failure mode is a server
// that cannot start -- every restart reaches the same statement and dies there,
// with no downgrade path. Rewriting the scripts would fix new installs and leave
// those databases just as stuck, so the filter runs at apply time where it can
// see the actual schema.
//
// Only the two captured names are used, and only to run a bound parameterised
// lookup against the catalog; no part of the migration text reaches the
// database as a query. A statement the pattern does not match is passed through
// untouched, so an unrecognised form fails exactly as it did before rather than
// being silently dropped.
func skipPresentColumns(tx *sqlx.Tx, dia dialect, script string) (string, error) {
	if !strings.Contains(strings.ToUpper(script), "ADD COLUMN") {
		return script, nil
	}

	lines := strings.Split(script, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		m := addColumnRe.FindStringSubmatch(line)
		if m == nil {
			kept = append(kept, line)
			continue
		}

		var present int
		if err := tx.Get(&present, dia.q(dia.columnExistsQuery()), m[1], m[2]); err != nil {
			return "", fmt.Errorf("inspect %s.%s: %w", m[1], m[2], err)
		}
		if present > 0 {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n"), nil
}

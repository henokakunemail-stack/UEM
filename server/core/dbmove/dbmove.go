// Package dbmove copies a SQLite database into PostgreSQL, for moving a
// single-node installation onto the database a multi-node one needs.
package dbmove

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// Options configure one move.
type Options struct {
	// From is the SQLite database file. It is never written: the copy is read
	// from a snapshot taken with VACUUM INTO.
	From string
	// To is the PostgreSQL URL (postgres://user:pass@host/db?sslmode=...).
	To string
	// Force allows copying into a database that already holds users or
	// devices. Everything copied over is replaced.
	Force bool
	// Logf reports progress. nil discards it.
	Logf func(format string, args ...any)
}

// TableResult is one table's outcome.
type TableResult struct {
	Table  string
	Source int
	Target int
}

// Result is the move's outcome.
type Result struct {
	Tables []TableResult
	// AuditChainOK is whether the copied audit log verifies on PostgreSQL.
	AuditChainOK bool
	// SourceChainOK is whether it verified on SQLite before the move; a log
	// that was already broken cannot be blamed on the move.
	SourceChainOK bool
}

// Move copies every table from SQLite into PostgreSQL.
//
//  1. Snapshot the source with VACUUM INTO and migrate the snapshot to the
//     current schema, so an installation one release behind moves too and the
//     original file is untouched.
//  2. Migrate PostgreSQL to the same schema.
//  3. Refuse if PostgreSQL already holds users or devices, unless Force.
//  4. Empty the target tables (the migrations seed a few), then copy each
//     table in foreign-key order, one transaction per table.
//  5. Compare row counts and verify the audit hash chain on PostgreSQL.
func Move(ctx context.Context, opt Options) (*Result, error) {
	logf := opt.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if opt.From == "" || opt.To == "" {
		return nil, errors.New("both the SQLite file and the PostgreSQL URL are required")
	}
	if _, err := os.Stat(opt.From); err != nil {
		return nil, fmt.Errorf("source database: %w", err)
	}

	snapDir, err := os.MkdirTemp("", "uem-move-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(snapDir)
	snapPath := filepath.Join(snapDir, "snapshot.db")

	logf("snapshotting %s", opt.From)
	if err := snapshot(opt.From, snapPath); err != nil {
		return nil, fmt.Errorf("snapshot source: %w", err)
	}
	src, err := db.Open(snapPath) // migrates the snapshot, never the original
	if err != nil {
		return nil, fmt.Errorf("open snapshot: %w", err)
	}
	defer src.Close()

	logf("connecting to PostgreSQL and applying the schema")
	dst, err := db.OpenWithDriver("postgres", opt.To)
	if err != nil {
		return nil, fmt.Errorf("open target: %w", err)
	}
	defer dst.Close()

	if !opt.Force {
		for _, t := range []string{"users", "devices"} {
			var n int
			if err := dst.GetContext(ctx, &n, "SELECT COUNT(*) FROM "+t); err != nil {
				return nil, fmt.Errorf("inspect target %s: %w", t, err)
			}
			if n > 0 {
				return nil, fmt.Errorf("the target database already has %d rows in %s; "+
					"refusing to overwrite it (pass -force to replace its contents)", n, t)
			}
		}
	}

	tables, err := tablesInDependencyOrder(ctx, src)
	if err != nil {
		return nil, err
	}

	res := &Result{}
	srcReport, err := audit.VerifyChain(ctx, src)
	res.SourceChainOK = err == nil && srcReport.OK

	logf("emptying %d target tables", len(tables))
	quoted := make([]string, len(tables))
	for i, t := range tables {
		quoted[i] = `"` + t + `"`
	}
	if _, err := dst.ExecContext(ctx, "TRUNCATE TABLE "+strings.Join(quoted, ", ")+" CASCADE"); err != nil {
		return nil, fmt.Errorf("empty target tables: %w", err)
	}

	for _, t := range tables {
		n, err := copyTable(ctx, src, dst, t)
		if err != nil {
			return nil, fmt.Errorf("copy %s: %w", t, err)
		}
		var target int
		if err := dst.GetContext(ctx, &target, `SELECT COUNT(*) FROM "`+t+`"`); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		res.Tables = append(res.Tables, TableResult{Table: t, Source: n, Target: target})
		logf("  %-36s %8d rows", t, target)
		if target != n {
			return res, fmt.Errorf("table %s: copied %d rows but the target holds %d", t, n, target)
		}
	}

	report, err := audit.VerifyChain(ctx, dst)
	if err != nil {
		return res, fmt.Errorf("verify audit chain on PostgreSQL: %w", err)
	}
	res.AuditChainOK = report.OK
	if res.SourceChainOK && !report.OK {
		return res, fmt.Errorf("the audit chain verified on SQLite but not after the move: %s", report.Reason)
	}
	return res, nil
}

// snapshot writes a consistent copy of the source with VACUUM INTO. It reads
// the source through a read-only connection, so a server that is still running
// keeps working and the file itself is never modified.
func snapshot(from, to string) error {
	abs, err := filepath.Abs(from)
	if err != nil {
		return err
	}
	ro, err := sql.Open("sqlite", "file:"+filepath.ToSlash(abs)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer ro.Close()
	_, err = ro.Exec(`VACUUM INTO ?`, to)
	return err
}

// tablesInDependencyOrder lists the application tables with every table after
// the tables its foreign keys point to, so rows can be inserted with the
// constraints checked as they go.
func tablesInDependencyOrder(ctx context.Context, src *sqlx.DB) ([]string, error) {
	var names []string
	if err := src.SelectContext(ctx, &names, `
		SELECT name FROM sqlite_master
		 WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'
		 ORDER BY name`); err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	deps := map[string][]string{}
	for _, t := range names {
		rows, err := src.QueryxContext(ctx, `SELECT "table" FROM pragma_foreign_key_list(?)`, t)
		if err != nil {
			return nil, fmt.Errorf("foreign keys of %s: %w", t, err)
		}
		for rows.Next() {
			var ref string
			if err := rows.Scan(&ref); err == nil && ref != t {
				deps[t] = append(deps[t], ref)
			}
		}
		rows.Close()
	}

	var order []string
	state := map[string]int{} // 0 unvisited, 1 visiting, 2 done
	var visit func(string) error
	visit = func(t string) error {
		switch state[t] {
		case 1:
			return fmt.Errorf("foreign keys form a cycle through %s", t)
		case 2:
			return nil
		}
		state[t] = 1
		refs := append([]string(nil), deps[t]...)
		sort.Strings(refs)
		for _, r := range refs {
			if err := visit(r); err != nil {
				return err
			}
		}
		state[t] = 2
		order = append(order, t)
		return nil
	}
	for _, t := range names {
		if err := visit(t); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// copyTable copies the columns both sides have, in one transaction. A column
// only one side has is skipped: the schemas come from the same migrations, so
// that is a column a future migration added and the other side defaults.
func copyTable(ctx context.Context, src, dst *sqlx.DB, table string) (int, error) {
	var srcCols []string
	if err := src.SelectContext(ctx, &srcCols, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table); err != nil {
		return 0, err
	}
	var dstCols []string
	if err := dst.SelectContext(ctx, &dstCols,
		`SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ?`,
		table); err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, c := range dstCols {
		have[c] = true
	}
	var cols []string
	for _, c := range srcCols {
		if have[c] {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return 0, nil
	}
	isAudit := table == "audit_logs"

	quotedCols := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = `"` + c + `"`
	}
	rows, err := src.QueryxContext(ctx, `SELECT `+strings.Join(quotedCols, ", ")+` FROM "`+table+`"`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	insertCols := quotedCols
	if isAudit && have["hash_time"] && !contains(cols, "hash_time") {
		insertCols = append(append([]string(nil), quotedCols...), `"hash_time"`)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(insertCols)), ", ")

	tx, err := dst.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PreparexContext(ctx, `INSERT INTO "`+table+`" (`+strings.Join(insertCols, ", ")+`) VALUES (`+placeholders+`)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	createdIdx, hashTimeIdx := indexOf(cols, "created_at"), indexOf(cols, "hash_time")
	n := 0
	for rows.Next() {
		vals, err := rows.SliceScan()
		if err != nil {
			return n, err
		}
		if isAudit {
			vals = withHashTime(vals, createdIdx, hashTimeIdx, len(insertCols) > len(cols))
		}
		if _, err := stmt.ExecContext(ctx, vals...); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	return n, tx.Commit()
}

// withHashTime gives an audit row the exact timestamp text it was hashed over,
// taken from SQLite's full-precision created_at before PostgreSQL rounds it to
// microseconds. A row that already records it keeps its own.
func withHashTime(vals []any, createdIdx, hashTimeIdx int, appended bool) []any {
	exact := ""
	if createdIdx >= 0 {
		if t, ok := vals[createdIdx].(time.Time); ok {
			exact = t.UTC().Format(time.RFC3339Nano)
		}
	}
	if appended {
		return append(vals, nullIfEmpty(exact))
	}
	if hashTimeIdx >= 0 {
		if cur, ok := vals[hashTimeIdx].(string); !ok || cur == "" {
			vals[hashTimeIdx] = nullIfEmpty(exact)
		}
	}
	return vals
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func contains(xs []string, x string) bool { return indexOf(xs, x) >= 0 }

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

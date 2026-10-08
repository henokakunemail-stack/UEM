package db

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jmoiron/sqlx"
)

// Registering the SQLite driver name with sqlx's bind-type table is what makes
// db.Rebind usable for both databases. Without it BindType("sqlite") is
// UNKNOWN, Rebind returns the query unchanged, and the ? placeholders would
// survive a round trip that a PostgreSQL caller has every reason to believe was
// rewritten -- the query runs, binds nothing, and returns no rows.
//
// Done at init rather than per Open because the table is package-level global
// state in sqlx, and registering the same key twice is a no-op.
func init() {
	sqlx.BindDriver("sqlite", sqlx.QUESTION)
	sqlx.BindDriver("postgres", sqlx.DOLLAR)
}

// The migrations are written once, in SQLite dialect, and translated on the way
// to another database. Keeping a second checked-in copy per dialect is what
// makes dual-dialect support rot: the copies drift, and nothing notices until a
// release adds a column to one of them and not the other.
//
// Every difference below was confirmed against a live PostgreSQL 16 server
// rather than inferred, because three of them fail in ways that read like a
// network problem:
//
//	DATETIME          -> ERROR: type "datetime" does not exist
//	INSERT OR IGNORE  -> ERROR: syntax error at or near "OR"
//	?                 -> stays a literal, no error, silently wrong
type dialect struct {
	// name is the driver name, used to pick the bind type.
	name string
	// postgres is the one flag that selects the whole alternative behaviour.
	postgres bool
}

// dialectFor returns the dialect for a driver name. An unrecognised name falls
// back to SQLite, which is the default and the only one that has to keep
// working; failing loudly on a typo would be safer but would also mean a config
// that merely names a driver nobody uses cannot start at all.
func dialectFor(driverName string) dialect {
	switch strings.ToLower(driverName) {
	case "postgres", "postgresql":
		return dialect{name: driverName, postgres: true}
	default:
		return dialect{name: "sqlite"}
	}
}

// timeType is the column type for an instant. TIMESTAMPTZ rather than TIMESTAMP
// because every timestamp in these schemas is an absolute UTC instant, and a
// timestamp without a zone is interpreted in the session time zone -- which
// makes a stored value mean different things to two servers in different
// regions, and to a server after a DST change.
func (d dialect) timeType() string {
	if d.postgres {
		return "TIMESTAMPTZ"
	}
	return "DATETIME"
}

// schemaMigrationsDDL is the bookkeeping table. It is written in dialect rather
// than embedded in a migration file because it has to exist before any migration
// can run.
func (d dialect) schemaMigrationsDDL() string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at %s NOT NULL
	)`, d.timeType())
}

// q rewrites a bind placeholder for this dialect.
//
// The migrations, and 83 call sites across the server, all use '?'. sqlx
// already knows how to convert that to $N for PostgreSQL, but only if the
// driver name is in its bind-type table -- and it looks for "sqlite3" and
// "postgres", while this codebase registers the SQLite driver as "sqlite". The
// registration in init makes both names resolve, so a query built here is
// correct on either database without the call sites changing.
//
// On SQLite this is the identity: '?' is what that driver expects, and
// converting it would break every existing query.
func (d dialect) q(query string) string {
	if !d.postgres {
		return query
	}
	return sqlx.Rebind(sqlx.BindType("postgres"), query)
}

// columnExistsQuery asks the catalog whether a column is present.
//
// Two different catalogs because the two databases disagree on where they keep
// it, and neither has a portable alternative: pragma_table_info is a SQLite
// table-valued function, information_schema is the SQL standard view that
// SQLite does not implement. The bound parameters are the table and column
// names, in that order, on both paths.
func (d dialect) columnExistsQuery() string {
	if d.postgres {
		return `SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name = ? AND column_name = ?`
	}
	return `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`
}

// translate converts a SQLite-dialect migration into this dialect.
//
// It is a textual pass over the script, which is only safe because the
// differences are lexical: a type name, an upsert clause, a bind placeholder.
// Nothing here interprets SQL, so a statement it does not recognise reaches the
// database exactly as written and fails there if it is wrong.
func (d dialect) translate(script string) string {
	if !d.postgres {
		return script
	}
	s := script
	s = datetimeTypeRe.ReplaceAllString(s, "TIMESTAMPTZ")
	// SQLite attaches the conflict clause to the INSERT keyword; PostgreSQL
	// attaches it after the whole VALUES list. Both halves are matched in one
	// pass so the clause lands at the end of the very statement it belongs to:
	// the nine templates in 0018 carry long multi-row values bodies, and a
	// rewrite that stopped at the VALUES keyword would splice ON CONFLICT into
	// the middle of a row list and fail with "syntax error at or near ON".
	s = insertOrIgnoreRe.ReplaceAllString(s, "INSERT INTO ${1} (${2}) ${3} ON CONFLICT DO NOTHING;")
	return s
}

var (
	// Word boundaries keep this off identifiers like my_datetime_column. Every
	// occurrence in the migration set is upper case, so no case folding is
	// needed and none is done -- matching loosely here would risk rewriting a
	// string literal that happens to contain the word.
	datetimeTypeRe = regexp.MustCompile(`\bDATETIME\b`)
	// INSERT OR IGNORE INTO <table> (<columns>) VALUES <values...>;
	// Group 1 is the table, 2 the column list, 3 everything from VALUES up to
	// but not including the terminating semicolon -- so the conflict clause
	// lands after the whole value list, which is where PostgreSQL requires it,
	// and before the statement ends.
	insertOrIgnoreRe = regexp.MustCompile(
		`(?is)INSERT\s+OR\s+IGNORE\s+INTO\s+(\w+)\s*\(([^)]*)\)\s*(VALUES\b.*?);`)
)

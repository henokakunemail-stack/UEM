package db

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, no cgo
)

// Pool sizing. WAL allows any number of readers alongside one writer, so the
// pool is sized for the reads: a console page issues several at once, and
// serialising them behind one connection is what turns a slow write batch into
// a whole-page timeout.
//
// The numbers are deliberately modest. Every connection is a separate SQLite
// page cache over the same file, so a large pool trades memory for
// concurrency this workload never uses; and the write path is serialised by
// SQLite regardless of how wide the pool is.
const (
	maxOpenConns = 16
	maxIdleConns = 4
)

// dsn builds the connection string.
//
// Every setting lives here, in the DSN, rather than in an Exec after Open, and
// that placement is the whole point. A PRAGMA run through the pool applies to
// exactly one connection -- whichever one the pool happened to hand out. The
// pool then opens more connections for later requests, and those are created
// with the default settings: no WAL, no busy_timeout, no foreign keys. The
// first one keeps the settings and the rest silently do not, so `busy_timeout`
// vanishes exactly when concurrency needs it, which is the moment it matters.
//
// modernc.org/sqlite applies these parameters in newConn for every connection
// it opens (see applyQueryParams), so putting them in the DSN is what makes
// them properties of the database rather than of whichever connection ran
// first. Verified against driver.go's documented key list rather than assumed:
// _txlock, _busy_timeout, _journal_mode, _foreign_keys, _synchronous.
func dsn(dbPath string) string { return DSNForPath(dbPath) }

// DSNForPath builds the connection string for a database path. Exported so a
// test that needs a second handle on the same file -- a child process, a
// restored backup -- can be configured exactly as production is, instead of
// hand-rolling an Open plus a PRAGMA that lands on one connection.
func DSNForPath(dbPath string) string {
	q := url.Values{}
	// WAL: readers do not block on the writer, and a reader crash cannot corrupt
	// the database.
	q.Set("_journal_mode", "WAL")
	// Bounds how long a writer waits for the write lock instead of failing
	// immediately with SQLITE_BUSY. This is what the pool size relies on for
	// write contention.
	q.Set("_busy_timeout", "5000")
	// Foreign keys are per-connection in SQLite, not per-database. Enforcing
	// them on one connection out of a pool is worse than not enforcing them at
	// all: a write can pass on the connection that has them on and fail on the
	// one that does not, which is the opposite of a constraint.
	q.Set("_foreign_keys", "1")
	q.Set("_synchronous", "NORMAL")
	// Every transaction this codebase opens is a batch that reads then writes,
	// so it takes SQLite's write lock at BEGIN rather than at first write. A
	// deferred transaction would read outside the write lock and only collide on
	// the INSERT, where the loser has to unwind a transaction built on a read
	// that is already stale. Taking the lock up front makes the read part of
	// the atomic unit, which is what audit.writeChained depends on -- see the
	// comment there on why the chain's read-prev and insert cannot be separated.
	//
	// This is the driver honoured for every BeginTx on every connection, so
	// writeChained no longer has to issue BEGIN IMMEDIATE by hand, and the other
	// eleven transaction sites in this codebase get the same guarantee for free.
	q.Set("_txlock", "immediate")
	// No "file:" prefix. The driver strips the query itself and hands SQLite a
	// plain filename, which is what keeps a Windows path from being re-parsed as
	// a URI (SQLITE_OPEN_URI is set, so a "file:" prefix would move the parsing
	// into SQLite and turn any "?" in the path into a parameter boundary).
	return dbPath + "?" + q.Encode()
}

// Open creates/opens the SQLite database and applies migrations.
// Uses WAL mode + busy_timeout for concurrent reader/writer access.
func Open(dbPath string) (*sqlx.DB, error) {
	// Make sure the data directory exists (e.g. ./data).
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir %s: %w", dir, err)
		}
	}

	d, err := sqlx.Open("sqlite", dsn(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	// WAL lets readers run while a writer holds the write lock, so the pool is
	// sized for concurrent reads rather than serialised through one connection.
	//
	// The single-writer rule still holds, but it is enforced by SQLite itself
	// plus the busy_timeout in the DSN, not by collapsing the pool to one
	// connection. Collapsing it was what made every `Connx` call a deadlock
	// waiting on a connection the caller already held, and there is no way to
	// hold a dedicated connection out of a one-connection pool: the *sql.Conn
	// comes from the same pool. Readers also stop being able to make progress
	// while a batch transaction runs, which is the shape a 10k-endpoint fleet
	// turns into a stalled console.
	//
	// Writers serialise on SQLite's write lock. busy_timeout is what bounds how
	// long a writer waits, and every transaction in this codebase is a batch
	// that commits well inside that, so a collision resolves into a wait rather
	// than a dropped write.
	d.SetMaxOpenConns(maxOpenConns)
	// Nothing here holds a transaction across a request, so an idle connection
	// can be returned to the pool and a later one opened to serve a different
	// request. Without this the pool would grow to hold the whole concurrency
	// spike for the process's lifetime, and each connection is its own SQLite
	// page cache against the same file.
	d.SetMaxIdleConns(maxIdleConns)
	d.SetConnMaxIdleTime(5 * time.Minute)

	if err := d.Ping(); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	if err := Migrate(d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

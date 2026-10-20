package db

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Advisory lock keys. Each names one piece of work that only one server node
// may do at a time. The values are arbitrary but fixed: two nodes agree on a
// lock only by agreeing on its number, so a key is never reused or renumbered.
const (
	LockMigrate    int64 = 0x55454D0001 // applying migrations
	LockAuditChain int64 = 0x55454D0002 // appending to the audit hash chain
	LockLeader     int64 = 0x55454D0003 // background-loop leadership
)

// IsPostgres reports whether d talks to PostgreSQL. Everything that exists only
// to coordinate several server nodes keys off this: SQLite is one file owned by
// one process, so there is never a second node to coordinate with.
func IsPostgres(d *sqlx.DB) bool {
	return dialectFor(d.DriverName()).postgres
}

// WithAdvisoryLock runs fn while holding a PostgreSQL session advisory lock, so
// fn runs on at most one server node at a time. Other nodes block until it is
// released.
//
// The lock belongs to a database session, so it is taken on a connection held
// for the whole call and released on that same connection; taking it through
// the pool would lock one connection and unlock another, which releases
// nothing.
//
// On SQLite it simply runs fn. SQLite has no second node, and its writers are
// already serialised by the file lock (_txlock=immediate in the DSN).
func WithAdvisoryLock(ctx context.Context, d *sqlx.DB, key int64, fn func() error) error {
	return withAdvisoryLock(ctx, d, IsPostgres(d), key, fn)
}

// withAdvisoryLock takes the dialect decision from the caller, for the
// migration runner, which is told its dialect rather than inferring it.
func withAdvisoryLock(ctx context.Context, d *sqlx.DB, postgres bool, key int64, fn func() error) error {
	if !postgres {
		return fn()
	}
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("advisory lock %#x: get connection: %w", key, err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(?)`, key); err != nil {
		return fmt.Errorf("advisory lock %#x: %w", key, err)
	}
	defer func() {
		// A fresh context: the caller's may already be cancelled, and an
		// unlock that is skipped holds the lock until the connection closes.
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(?)`, key)
	}()
	return fn()
}

// LockInTx takes a transaction-scoped advisory lock inside tx on PostgreSQL. It
// is released by the commit or rollback that ends tx, so there is nothing to
// unlock and nothing to forget. On SQLite it does nothing: the transaction
// itself already holds the database write lock.
func LockInTx(ctx context.Context, d *sqlx.DB, tx *sqlx.Tx, key int64) error {
	if !IsPostgres(d) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, key); err != nil {
		return fmt.Errorf("advisory xact lock %#x: %w", key, err)
	}
	return nil
}

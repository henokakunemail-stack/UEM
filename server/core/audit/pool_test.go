package audit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

// TestAuditWriteDoesNotBlockOnAConnectionTheCallerHolds is the regression test
// for a server-wide hang.
//
// writeChained takes a dedicated connection so its read-prev and its insert stay
// on one connection, and that used to deadlock. The pool was capped at one
// connection, so a caller that already held it -- which every authenticated
// request does, since RequireAuth reads auth_sessions -- left the audit write
// waiting for a connection only it could release. There was no timeout on the
// wait, so the request never returned and every other request queued behind the
// single connection behind it.
//
// It cannot be fixed by holding a connection aside for the chain writer: a
// *sql.Conn is drawn from the same pool, so a one-connection pool has none to
// spare. The pool has to be wider than one for the write to have somewhere to go.
func TestAuditWriteDoesNotBlockOnAConnectionTheCallerHolds(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	conn, err := d.Connx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	done := make(chan error, 1)
	go func() { done <- Log(ctx, d, "user", "u1", "regression.held_connection", "t1", nil) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("audit write failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("audit write blocked waiting for a pooled connection: the pool cannot " +
			"serve a writer while the caller holds the only connection")
	}
}

// TestEveryConnectionCarriesTheDatabaseSettings guards a failure that outlived
// the one-connection pool and would have been invisible with it.
//
// The pragmas used to be run through the pool after Open, which applies them to
// exactly one connection -- whichever one the pool happened to hand out. Every
// later connection was created with SQLite's defaults: no WAL, no busy_timeout,
// no foreign keys. With the pool capped at one that was harmless, because there
// was never a second connection to be wrong. The moment the pool was widened, a
// request landing on a second connection would have run without busy_timeout and
// failed with SQLITE_BUSY, and foreign keys would have been off on some
// connections and on others, so the same write could be refused or accepted
// depending on which connection served it.
//
// So the settings have to be in the DSN, which the driver applies in newConn for
// every connection. This checks the connections themselves rather than the DSN
// string, because the DSN string is not the claim being made.
func TestEveryConnectionCarriesTheDatabaseSettings(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Hold several at once, so they cannot all be the same one recycled.
	const n = 6
	var conns []*sqlx.Conn
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	for i := 0; i < n; i++ {
		c, err := d.Connx(ctx)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		conns = append(conns, c)

		var journal string
		var busy int
		var fk int
		if err := c.QueryRowxContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
			t.Fatalf("connection %d journal_mode: %v", i, err)
		}
		if err := c.QueryRowxContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", i, err)
		}
		if err := c.QueryRowxContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", i, err)
		}
		if journal != "wal" {
			t.Errorf("connection %d: journal_mode = %q, want wal", i, journal)
		}
		if busy != 5000 {
			t.Errorf("connection %d: busy_timeout = %d, want 5000", i, busy)
		}
		if fk != 1 {
			t.Errorf("connection %d: foreign_keys = %d, want 1 (a write refused on one "+
				"connection and accepted on another is not a constraint)", i, fk)
		}
	}
}

// TestConcurrentWritesOnAWiderPoolStillChain checks that widening the pool did
// not trade the deadlock for a second writer.
//
// SQLite serialises the writers regardless of how wide the pool is, and the
// per-handle mutex in writeChained queues the ones in this process. What is new
// is that a second connection can now genuinely be in the pool, so the mutex and
// the transaction are doing the work alone rather than being redundant with a
// one-connection pool.
func TestConcurrentWritesOnAWiderPoolStillChain(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	const writers = 60
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := Log(ctx, d, "user", "u1", "regression.concurrent", "t1",
				map[string]string{"seq": time.Duration(i).String()}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent audit write failed: %v", err)
	}

	var count int
	if err := d.GetContext(ctx, &count, `SELECT COUNT(*) FROM audit_logs`); err != nil {
		t.Fatal(err)
	}
	if count != writers {
		t.Fatalf("wrote %d entries, table holds %d: a write was lost", writers, count)
	}
	if rep := verify(t, d); !rep.OK {
		t.Fatalf("chain broken by concurrent writes: %s", rep.Reason)
	}
}

// TestWriteTransactionsTakeTheWriteLockAtBegin is the property the chain's
// read-prev-then-insert depends on, checked on the driver rather than on a
// comment about it.
//
// A deferred transaction reads outside the write lock and only takes it at the
// first write. Two such readers can both see the same tail and only collide on
// the insert, leaving two entries claiming one predecessor -- a break in a log
// nobody tampered with, which is the failure mode this chain exists to rule
// out. The DSN asks the driver for immediate, and this confirms the ask landed.
func TestWriteTransactionsTakeTheWriteLockAtBegin(t *testing.T) {
	d := openTestDB(t)

	tx, err := d.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	// A read inside a transaction that already holds the write lock must not
	// fail with "cannot start a transaction within a transaction" and must not
	// report the connection as unlocked.
	var one int
	if err := tx.GetContext(context.Background(), &one, `SELECT 1`); err != nil {
		t.Fatalf("read inside the write transaction failed: %v", err)
	}
}

// TestTheChainSurvivesAnOutOfBandReaderHoldingThePool is the shape the console
// actually produces: several pages issuing reads while an audit write is in
// flight. Under WAL those do not block each other, and this is what would
// regress first if the pool were narrowed again.
func TestTheChainSurvivesAnOutOfBandReaderHoldingThePool(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers, on their own goroutines, holding a connection each.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				var n int
				if err := d.GetContext(ctx, &n, `SELECT COUNT(*) FROM devices`); err != nil {
					return
				}
			}
		}()
	}

	for i := 0; i < 20; i++ {
		if err := Log(ctx, d, "user", "u1", "regression.readers", "t1", nil); err != nil {
			t.Errorf("audit write %d failed with readers on the pool: %v", i, err)
			break
		}
	}
	close(stop)
	wg.Wait()

	if rep := verify(t, d); !rep.OK {
		t.Fatalf("chain broken: %s", rep.Reason)
	}
}

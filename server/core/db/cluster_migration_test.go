package db

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

// rewindTo0026 makes a fully migrated database look like one that stopped at
// 0026, which is the state a real upgrade starts from. The columns 0027 adds
// are dropped where the dialect allows it, so the ALTER TABLE statements really
// run rather than being skipped as already present.
func rewindTo0026(t *testing.T, d *sqlx.DB) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM schema_migrations WHERE version = '0027_cluster.sql'`,
		`DROP TABLE IF EXISTS server_nodes`,
		`DROP TABLE IF EXISTS node_outbox`,
		`DROP TABLE IF EXISTS login_failures`,
		`ALTER TABLE devices DROP COLUMN connected_node`,
		`ALTER TABLE devices DROP COLUMN connected_at`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func check0027Applied(t *testing.T, d *sqlx.DB) {
	t.Helper()
	dia := dialectFor(d.DriverName())
	for _, col := range []string{"connected_node", "connected_at"} {
		var n int
		if err := d.Get(&n, dia.q(dia.columnExistsQuery()), "devices", col); err != nil || n != 1 {
			t.Fatalf("devices.%s missing after 0027 (count=%d err=%v)", col, n, err)
		}
	}
	for _, table := range []string{"server_nodes", "node_outbox", "login_failures"} {
		var n int
		if err := d.Get(&n, "SELECT COUNT(*) FROM "+table); err != nil {
			t.Fatalf("table %s missing after 0027: %v", table, err)
		}
	}
	var recorded int
	if err := d.Get(&recorded, `SELECT COUNT(*) FROM schema_migrations WHERE version = '0027_cluster.sql'`); err != nil || recorded != 1 {
		t.Fatalf("0027 recorded %d times (err=%v)", recorded, err)
	}
}

func TestMigration0027UpgradesA0026DatabaseOnSQLite(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	rewindTo0026(t, d)
	if err := Migrate(d); err != nil {
		t.Fatalf("0027 on a 0026 database: %v", err)
	}
	check0027Applied(t, d)
	if err := Migrate(d); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	check0027Applied(t, d)
}

func TestPostgres_Migration0027UpgradesA0026Database(t *testing.T) {
	d := openPostgresForTest(t)
	rewindTo0026(t, d)
	if err := Migrate(d); err != nil {
		t.Fatalf("0027 on a 0026 database: %v", err)
	}
	check0027Applied(t, d)
}

// Two server nodes starting together against one PostgreSQL must not both
// apply the same migration. Without the advisory lock one of them fails on the
// DDL or on the schema_migrations primary key and the node exits.
func TestPostgres_ConcurrentMigrateIsSerialised(t *testing.T) {
	d := openPostgresForTest(t)
	// Several rounds, each released at once from a barrier: the race is narrow,
	// and a single round with goroutines that start whenever the scheduler
	// gets to them passes with or without the lock.
	for round := 0; round < 5; round++ {
		rewindTo0026(t, d)
		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs <- Migrate(d)
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: concurrent migrate failed: %v", round, err)
			}
		}
		check0027Applied(t, d)
	}
}

// The lock has to actually exclude: two holders must never overlap.
func TestPostgres_AdvisoryLockExcludes(t *testing.T) {
	d := openPostgresForTest(t)
	var inside, maxInside atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithAdvisoryLock(context.Background(), d, LockLeader+1000, func() error {
				n := inside.Add(1)
				for {
					m := maxInside.Load()
					if n <= m || maxInside.CompareAndSwap(m, n) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				inside.Add(-1)
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if maxInside.Load() != 1 {
		t.Fatalf("%d holders were inside the lock at once", maxInside.Load())
	}
}

func TestAdvisoryLockIsANoOpOnSQLite(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "lock.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ran := false
	if err := WithAdvisoryLock(context.Background(), d, LockLeader, func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	tx, _ := d.Beginx()
	defer tx.Rollback()
	if err := LockInTx(context.Background(), d, tx, LockAuditChain); err != nil {
		t.Fatalf("LockInTx on SQLite: %v", err)
	}
	if IsPostgres(d) {
		t.Fatal("a SQLite database reported itself as PostgreSQL")
	}
}

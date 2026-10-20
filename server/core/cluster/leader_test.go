package cluster

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

func TestTheOnlySQLiteNodeIsLeader(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "leader.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	l := NewLeader(d)
	ctx, cancel := context.WithCancel(context.Background())
	elected := make(chan struct{})
	done := make(chan struct{})
	go func() {
		l.Run(ctx, func(context.Context) { close(elected) })
		close(done)
	}()
	select {
	case <-elected:
	case <-time.After(2 * time.Second):
		t.Fatal("a SQLite node was never elected")
	}
	if !l.IsLeader() {
		t.Fatal("IsLeader is false on the only node")
	}
	cancel()
	<-done
	if l.IsLeader() {
		t.Fatal("still leader after Run returned")
	}
}

// scratchPostgres creates an empty database for one test and drops it after.
func scratchPostgres(t *testing.T) string {
	t.Helper()
	base := os.Getenv("UEM_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("UEM_TEST_POSTGRES_URL not set")
	}
	admin, err := sqlx.Connect("postgres", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("uem_cluster_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c, err := sqlx.Connect("postgres", base); err == nil {
			_, _ = c.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
			c.Close()
		}
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	return u.String()
}

func openNode(t *testing.T, dsn string) *sqlx.DB {
	t.Helper()
	d, err := db.OpenWithDriver("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Exactly one of two nodes leads, and when the leader goes away the other takes
// over and starts the loops itself.
func TestPostgres_OneLeaderAndHandover(t *testing.T) {
	dsn := scratchPostgres(t)
	nodeA, nodeB := openNode(t, dsn), openNode(t, dsn)

	var runningA, runningB atomic.Int32
	elect := func(running *atomic.Int32) func(context.Context) {
		return func(ctx context.Context) {
			running.Add(1)
			go func() { <-ctx.Done(); running.Add(-1) }()
		}
	}

	la, lb := NewLeader(nodeA), NewLeader(nodeB)
	for _, l := range []*Leader{la, lb} {
		l.RetryEvery = 50 * time.Millisecond
		l.CheckEvery = 50 * time.Millisecond
	}
	ctxA, stopA := context.WithCancel(context.Background())
	ctxB, stopB := context.WithCancel(context.Background())
	defer stopB()
	doneA := make(chan struct{})
	go func() { la.Run(ctxA, elect(&runningA)); close(doneA) }()
	waitFor(t, "node A to lead", la.IsLeader)
	go lb.Run(ctxB, elect(&runningB))

	// B keeps trying and must keep failing while A leads.
	time.Sleep(300 * time.Millisecond)
	if lb.IsLeader() || runningB.Load() != 0 {
		t.Fatal("two nodes lead at once")
	}

	// A stops -- a shutdown, or a crash, which releases the lock the same way.
	stopA()
	<-doneA
	waitFor(t, "node A's loops to stop", func() bool { return runningA.Load() == 0 })
	waitFor(t, "node B to take over", func() bool { return lb.IsLeader() && runningB.Load() == 1 })
}

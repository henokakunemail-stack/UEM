package audit

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// scratchPostgres creates an empty database on the test server and returns its
// URL. It is dropped when the test ends.
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
	name := fmt.Sprintf("uem_audit_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := sqlx.Connect("postgres", base)
		if err == nil {
			_, _ = c.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
			c.Close()
		}
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// Two server nodes are two processes, each with its own *sqlx.DB and so its own
// in-process chain mutex. Writing concurrently through two handles is that
// situation: only a lock in the database keeps them from appending onto the
// same tail, and a forked chain is reported by VerifyChain as tampering.
func TestPostgres_TwoNodesWritingTheChainKeepItValid(t *testing.T) {
	dsn := scratchPostgres(t)
	nodeA, err := db.OpenWithDriver("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer nodeA.Close()
	nodeB, err := db.OpenWithDriver("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer nodeB.Close()

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		node := nodeA
		if i%2 == 1 {
			node = nodeB
		}
		wg.Add(1)
		go func(i int, node *sqlx.DB) {
			defer wg.Done()
			<-start
			if err := Log(context.Background(), node, "user", "u", "test.write", fmt.Sprint(i), nil); err != nil {
				t.Error(err)
			}
		}(i, node)
	}
	close(start)
	wg.Wait()

	report, err := VerifyChain(context.Background(), nodeA)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.Checked != 40 {
		t.Fatalf("chain written by two nodes does not verify: %+v", report)
	}
}

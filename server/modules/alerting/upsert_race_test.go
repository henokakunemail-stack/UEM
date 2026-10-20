package alerting

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// Concurrent triggers for one rule and device -- overlapping evaluations, or two
// server nodes -- end as one incident with every trigger counted, and none of
// them reports an error. Before, the loser's insert hit idx_alert_dedup and the
// trigger was dropped with an error in the log.
func TestConcurrentTriggersOpenOneIncidentAndCountEveryTrigger(t *testing.T) {
	h, database := newRuleFixture(t)
	concurrentTriggers(t, h, database)
}

// On SQLite writes are serialised and the race almost never happens; on
// PostgreSQL it is real, which is where this test proves the fallback.
func TestPostgres_ConcurrentTriggersOpenOneIncident(t *testing.T) {
	base := os.Getenv("UEM_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("UEM_TEST_POSTGRES_URL not set")
	}
	admin, err := sqlx.Connect("postgres", base)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("uem_alert_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	admin.Close()
	t.Cleanup(func() {
		if c, err := sqlx.Connect("postgres", base); err == nil {
			_, _ = c.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
			c.Close()
		}
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	database, err := db.OpenWithDriver("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo := NewRepository(database)
	for round := 0; round < 5; round++ {
		h := NewHandler(repo, NewEvaluator(database, repo), &stubAudit{}, nil)
		if _, err := database.Exec(`DELETE FROM alert_incidents`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`DELETE FROM alert_rules`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`DELETE FROM devices`); err != nil {
			t.Fatal(err)
		}
		concurrentTriggers(t, h, database)
	}
}

func concurrentTriggers(t *testing.T, h *Handler, database *sqlx.DB) {
	t.Helper()
	ctx := context.Background()

	rule := &AlertRule{Name: "disk", RuleType: "disk_low", ThresholdVal: 10, Severity: "warning", IsEnabled: true, CreatedBy: "u1"}
	if err := h.repo.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := database.Exec(database.Rebind(`INSERT INTO devices (id, hostname, os_name, status, enrolled_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-1','h','windows','online',?,'x',?,?)`), now, now, now); err != nil {
		t.Fatal(err)
	}

	const n = 12
	var created atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, isNew, err := h.repo.UpsertIncident(ctx, rule.ID, "dev-1", "warning", "Disk low", "msg")
			if err != nil {
				t.Error(err)
				return
			}
			if isNew {
				created.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if created.Load() != 1 {
		t.Fatalf("%d triggers reported a new incident, want exactly 1 (one webhook)", created.Load())
	}
	var incidents, triggers int
	if err := database.QueryRow(`SELECT COUNT(*), COALESCE(SUM(trigger_count),0) FROM alert_incidents WHERE status != 'resolved'`).
		Scan(&incidents, &triggers); err != nil {
		t.Fatal(err)
	}
	if incidents != 1 || triggers != n {
		t.Fatalf("incidents=%d triggers=%d, want 1 incident counting %d triggers", incidents, triggers, n)
	}
}

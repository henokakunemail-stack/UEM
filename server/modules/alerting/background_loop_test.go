package alerting

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// newTickingEvaluator builds an evaluator whose evaluation is observable.
//
// The bug this pins is that StartBackgroundEvaluator launched a goroutine and
// returned, so a `defer ticker.Stop()` on the function's own line fired the
// moment it returned. A stopped time.Ticker never delivers again, so the
// goroutine stayed alive, selected correctly, and was correctly stoppable by
// its context -- while being handed a channel that would never produce a value.
// No alert rule was ever evaluated for the life of the process.
//
// It is invisible from the API: the rules look configured and the incidents
// never arrive. So the observable has to be something the loop itself writes.
func newTickingEvaluator(t *testing.T) (*Evaluator, *sqlx.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "evaluator.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC()
	if _, err := database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
			device_secret_hash, enrolled_at, created_at, updated_at)
		VALUES ('d1','offline-box','windows','11','1.0.0','offline','x',?,?,?)`,
		now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO alert_rules (id, name, rule_type, threshold_val, severity,
			webhook_url, is_enabled, created_by, created_at, updated_at)
		VALUES ('r1','offline','device_offline',0,'info','',1,'u1',?,?)`,
		now, now); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(database)
	return NewEvaluator(database, repo), database
}

// TestTheBackgroundEvaluatorActuallyEvaluates is the regression test.
//
// It runs the real function and then watches for the row only that loop can
// produce. With the ticker stopped before its first fire, nothing is ever
// written and the wait times out.
func TestTheBackgroundEvaluatorActuallyEvaluates(t *testing.T) {
	e, database := newTickingEvaluator(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.StartBackgroundEvaluator(ctx, 20*time.Millisecond)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := database.Get(&count, `SELECT COUNT(*) FROM alert_incidents`); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	var count int
	_ = database.Get(&count, `SELECT COUNT(*) FROM alert_incidents`)
	t.Fatalf("the background evaluator wrote %d incidents in 5s with a 20ms "+
		"interval and an offline device to report: its ticker delivered nothing, "+
		"so no rule was ever evaluated", count)
}

// TestTheEvaluatorStopsWhenItsContextIsCancelled keeps the stop path honest in
// the other direction. Cancelling has to end the loop; a goroutine that
// outlives its context outlives the process.
func TestTheEvaluatorStopsWhenItsContextIsCancelled(t *testing.T) {
	e, database := newTickingEvaluator(t)

	ctx, cancel := context.WithCancel(context.Background())
	e.StartBackgroundEvaluator(ctx, 20*time.Millisecond)

	// Let it do some work first, so the loop is demonstrably running.
	waitForIncidents(t, database, 1)

	// The count must freeze: a loop that ignores its context keeps writing.
	settled := incidentCount(t, database)
	cancel()
	time.Sleep(200 * time.Millisecond)
	if after := incidentCount(t, database); after != settled {
		t.Errorf("incidents went from %d to %d after the context was cancelled: "+
			"the evaluator kept evaluating", settled, after)
	}
}

func waitForIncidents(t *testing.T, database *sqlx.DB, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if incidentCount(t, database) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no incidents after 5s, want at least %d", want)
}

func incidentCount(t *testing.T, database *sqlx.DB) int {
	t.Helper()
	var count int
	if err := database.Get(&count, `SELECT COUNT(*) FROM alert_incidents`); err != nil {
		t.Fatal(err)
	}
	return count
}

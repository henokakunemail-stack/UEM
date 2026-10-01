package taskscheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// pollFixture opens a database holding one enabled interval schedule that is
// already due, so a live poller fires it on its first tick.
func pollFixture(t *testing.T) (*sqlx.DB, *Scheduler, string) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "poll.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC()
	if _, err := database.Exec(`
		INSERT INTO script_templates (id, name, script_type, script_content, sha256_hash,
			timeout_seconds, created_by, created_at, updated_at)
		VALUES ('script-1','s','powershell','echo hi','abc',300,'u1',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	// last_run_at two hours ago with a 60-minute interval is already overdue.
	if _, err := database.Exec(`
		INSERT INTO task_schedules (id, name, script_id, target_type, target_id,
			schedule_type, schedule_expr, is_enabled, last_run_at, created_by,
			created_at, updated_at)
		VALUES ('sched-poll','due','script-1','all','','interval','60',1,?,'u1',?,?)`,
		now.Add(-2*time.Hour), now, now); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(database)
	return database, NewScheduler(repo, stubHub{}), "sched-poll"
}

// TestTheBackgroundSchedulerActuallyPolls is the regression test for a stop path
// that broke the thing it was meant to protect.
//
// StartBackgroundScheduler launches a goroutine and returns, so a `defer
// ticker.Stop()` on the function's own line fired immediately -- and a stopped
// time.Ticker never delivers again. The poller stayed alive, selected correctly
// and was correctly stoppable by its context, while holding a dead channel.
//
// The result is worse than the bug it replaced. Before, a duplicate poller could
// dispatch a script twice. After, no scheduled task ever ran at all: an operator
// enabled a schedule, saw it listed as enabled with a next run time, and nothing
// happened, every time, with nothing in the log.
func TestTheBackgroundSchedulerActuallyPolls(t *testing.T) {
	database, sched, _ := pollFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.StartBackgroundScheduler(ctx, 20*time.Millisecond)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := database.Get(&count,
			`SELECT COUNT(*) FROM scheduled_task_runs WHERE schedule_id = 'sched-poll'`); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	var count int
	_ = database.Get(&count, `SELECT COUNT(*) FROM scheduled_task_runs WHERE schedule_id = 'sched-poll'`)
	t.Fatalf("the background poller created %d runs in 5s for a schedule that was "+
		"already two hours overdue: its ticker delivered nothing, so no scheduled "+
		"task would ever have run", count)
}

// TestTheSchedulerStopsWhenItsContextIsCancelled keeps the stop path honest: a
// poller that ignores its context outlives the process and keeps dispatching
// into a server that is shutting down.
func TestTheSchedulerStopsWhenItsContextIsCancelled(t *testing.T) {
	database, sched, _ := pollFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	sched.StartBackgroundScheduler(ctx, 20*time.Millisecond)

	waitForRuns(t, database, 1)

	// last_run_at was just written by the poll that fired, so the schedule is no
	// longer due. Push it back into the past: if the poller were still running it
	// would dispatch again, and the count would move.
	settled := runCount(t, database)
	if _, err := database.Exec(
		`UPDATE task_schedules SET last_run_at = ? WHERE id = 'sched-poll'`,
		time.Now().UTC().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	cancel()
	time.Sleep(200 * time.Millisecond)

	if after := runCount(t, database); after != settled {
		t.Errorf("runs went from %d to %d after cancellation: the poller kept "+
			"dispatching after its context was cancelled", settled, after)
	}
}

func waitForRuns(t *testing.T, database *sqlx.DB, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runCount(t, database) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no runs after 5s, want at least %d", want)
}

func runCount(t *testing.T, database *sqlx.DB) int {
	t.Helper()
	var count int
	if err := database.Get(&count,
		`SELECT COUNT(*) FROM scheduled_task_runs WHERE schedule_id = 'sched-poll'`); err != nil {
		t.Fatal(err)
	}
	return count
}

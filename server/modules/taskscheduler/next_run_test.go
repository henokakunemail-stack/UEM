package taskscheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// openScheduleDB opens a database with one script template seeded, because
// task_schedules.script_id is a foreign key and an empty one is refused.
func openScheduleDB(t *testing.T, name string) *sqlx.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), name))
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
	return database
}

func intervalSchedule(expr string, enabled bool) *TaskSchedule {
	return &TaskSchedule{
		Name:         "nightly",
		ScriptID:     "script-1",
		TargetType:   "all",
		ScheduleType: "interval",
		ScheduleExpr: expr,
		IsEnabled:    enabled,
		CreatedBy:    "u1",
	}
}

// TestTheNextRunColumnIsFilled is the regression test for a column the console
// displayed but nothing populated.
//
// next_run_at was in the schema and on both schedule SELECTs, and the
// "Next run" column rendered sch.next_run_at -- so it read as an em dash for
// every schedule in every installation, and no amount of looking at the
// schedules page would explain why. The poller does not use the column either;
// it compares last_run_at against the interval -- so the value existed purely to
// tell the operator when a task would next fire, and it never once did.
func TestTheNextRunColumnIsFilled(t *testing.T) {
	repo := NewRepository(openScheduleDB(t, "next-run.db"))

	before := time.Now().UTC()
	s := intervalSchedule("30", true)
	if err := repo.CreateSchedule(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	stored, err := repo.GetScheduleByID(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.NextRunAt == nil {
		t.Fatal("next_run_at is NULL on a schedule that fires every 30 minutes; " +
			"the console's Next run column is permanently an em dash")
	}
	want := before.Add(30 * time.Minute)
	if diff := stored.NextRunAt.Sub(want); diff < -time.Minute || diff > time.Minute {
		t.Errorf("next_run_at = %v, want about %v (30 minutes after creation)",
			stored.NextRunAt, want)
	}
}

// TestDisablingAScheduleClearsItsNextRun keeps a stale value from outliving the
// thing it described. An operator pausing a schedule and still seeing a next
// fire time would reasonably conclude the pause did not take.
func TestDisablingAScheduleClearsItsNextRun(t *testing.T) {
	repo := NewRepository(openScheduleDB(t, "next-run-paused.db"))

	s := intervalSchedule("30", true)
	if err := repo.CreateSchedule(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	s.IsEnabled = false
	if err := repo.UpdateSchedule(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	stored, err := repo.GetScheduleByID(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.NextRunAt != nil {
		t.Errorf("next_run_at = %v on a disabled schedule; the operator paused it "+
			"and the console still promises a run", stored.NextRunAt)
	}
}

// TestChangingTheIntervalMovesTheNextRun is the other direction: the column has
// to follow the schedule, not just be written once.
func TestChangingTheIntervalMovesTheNextRun(t *testing.T) {
	repo := NewRepository(openScheduleDB(t, "next-run-changed.db"))

	s := intervalSchedule("60", true)
	if err := repo.CreateSchedule(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	s.ScheduleExpr = "5"
	updatedAt := time.Now().UTC()
	if err := repo.UpdateSchedule(context.Background(), s); err != nil {
		t.Fatal(err)
	}

	stored, err := repo.GetScheduleByID(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.NextRunAt == nil {
		t.Fatal("next_run_at is NULL after changing the interval to every 5 minutes")
	}
	if diff := stored.NextRunAt.Sub(updatedAt.Add(5 * time.Minute)); diff < -time.Minute || diff > time.Minute {
		t.Errorf("next_run_at = %v, want about %v: the column still describes the "+
			"old 60-minute interval", stored.NextRunAt, updatedAt.Add(5*time.Minute))
	}
}

// TestFiringAScheduleAdvancesItsNextRun closes the gap the three tests above
// cannot see: they all stop at creation and edit. The column has to keep
// tracking through the schedule's life, because CreateRun writes last_run_at on
// every dispatch and next_run_at used to be written only at create and update.
//
// A schedule created at 09:00 for every 60 minutes would otherwise still read
// "10:00" after the 10:00 run dispatched, while the real next fire was 11:00 --
// a stale promise that outlived the moment it described.
func TestFiringAScheduleAdvancesItsNextRun(t *testing.T) {
	database := openScheduleDB(t, "next-run-fired.db")
	repo := NewRepository(database)

	s := intervalSchedule("60", true)
	if err := repo.CreateSchedule(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	created, err := repo.GetScheduleByID(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if created.NextRunAt == nil {
		t.Fatal("next_run_at is NULL on a new interval schedule")
	}
	firstNext := *created.NextRunAt

	// The fire writes next_run_at = now + interval, and now is read again at the
	// top of CreateRun -- so the recorded value only moves if CreateRun's now is
	// strictly newer than the pre-run NextRunAt. A rounding delta between the two
	// reads can collapse that on a fast runner, making the column look un-moved.
	// Sleep long enough that the two reads cannot land in the same tick.
	time.Sleep(1100 * time.Millisecond)

	// Fire it the way the console's Run button does.
	if _, err := NewScheduler(repo, stubHub{}).TriggerSchedule(context.Background(), s.ID, "u1"); err != nil {
		t.Fatal(err)
	}

	after, err := repo.GetScheduleByID(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.NextRunAt == nil {
		t.Fatal("next_run_at is NULL after the schedule fired")
	}
	if !after.NextRunAt.After(firstNext) {
		t.Errorf("next_run_at = %v after firing, want later than the pre-run %v: "+
			"the column still promises the run that just happened",
			after.NextRunAt, firstNext)
	}
	if after.LastRunAt == nil {
		t.Fatal("last_run_at is NULL after firing")
	}
	want := after.LastRunAt.Add(60 * time.Minute)
	if diff := after.NextRunAt.Sub(want); diff < -time.Minute || diff > time.Minute {
		t.Errorf("next_run_at = %v, want about %v (one interval after the run just "+
			"happened)", after.NextRunAt, want)
	}
}

// TestANonIntervalScheduleHasNoNextRun: the column is an honest NULL for the
// shapes the scheduler does not run, not a stale carry-over.
func TestANonIntervalScheduleHasNoNextRun(t *testing.T) {
	s := &TaskSchedule{
		ScheduleType: "cron", ScheduleExpr: "0 0 * * *", IsEnabled: true,
	}
	if got := nextRunAt(s, time.Now().UTC()); got != nil {
		t.Errorf("next_run_at = %v for a cron schedule; this server does not run "+
			"cron, so there is no next fire to promise", got)
	}
}

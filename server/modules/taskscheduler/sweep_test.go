package taskscheduler

import (
	"context"
	"testing"
	"time"
)

// strand ages a device run past the sweep grace and marks its device offline,
// which is the state an endpoint reaches by losing power or its network
// mid-script: the command was delivered, nothing ever came back.
func (f *runFixture) strand(t *testing.T, deviceID, deviceRunID string) {
	t.Helper()
	if _, err := f.db.Exec(`UPDATE devices SET status = 'offline' WHERE id = ?`, deviceID); err != nil {
		t.Fatalf("mark %s offline: %v", deviceID, err)
	}
	if _, err := f.db.Exec(
		`UPDATE scheduled_task_device_runs SET started_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Hour), deviceRunID); err != nil {
		t.Fatalf("age %s: %v", deviceRunID, err)
	}
}

// TestARunStrandedByAVanishedAgentStillCloses is the regression test for a row
// nothing could ever move again.
//
// SyncRunStatus is called from exactly one place, the agent result handler, and
// it returns early unless every device run of the run is terminal. An endpoint
// that took the command and then lost power, lost its network, or had its agent
// replaced never posts a result, so its device run sat at 'dispatched' and the
// parent sat at 'running' with a NULL completed_at. There was no other caller
// and no sweeper, so the console showed a running scheduled task for the life
// of the installation, next to a device already marked offline.
func TestARunStrandedByAVanishedAgentStillCloses(t *testing.T) {
	f := newRunFixture(t, []string{"d1"})
	f.strand(t, "d1", f.deviceRunFor(t, "d1"))

	n, err := f.repo.AbandonOrphanedRuns(context.Background(), AbandonGrace)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("sweep reaped %d device runs, want 1", n)
	}

	status, completedAt := f.parent(t)
	if status != "failed" {
		t.Errorf("parent status = %q, want \"failed\": no endpoint can still be "+
			"running the script, so nothing about this run succeeded", status)
	}
	if completedAt == nil {
		t.Error("completed_at is NULL on a run that can never progress; the run " +
			"history keeps listing it as in progress forever")
	}
}

// TestASweepDoesNotReapARunThatIsStillWithinItsGrace keeps the sweep honest in
// the other direction: the grace exists so a machine that dropped off for a
// moment is never cut off.
func TestASweepDoesNotReapARunThatIsStillWithinItsGrace(t *testing.T) {
	f := newRunFixture(t, []string{"d1"})

	// Offline, but only just. AbandonGrace has not elapsed.
	if _, err := f.db.Exec(`UPDATE devices SET status = 'offline' WHERE id = 'd1'`); err != nil {
		t.Fatal(err)
	}

	if n, err := f.repo.AbandonOrphanedRuns(context.Background(), AbandonGrace); err != nil {
		t.Fatalf("sweep: %v", err)
	} else if n != 0 {
		t.Errorf("sweep reaped %d device runs, want 0: the grace has not elapsed", n)
	}
	if status, _ := f.parent(t); status != "running" {
		t.Errorf("parent status = %q, want \"running\"", status)
	}
}

// TestASweepDoesNotReapARunOnAnOnlineDevice is the other guard: elapsed time
// alone is not enough. A still-connected endpoint may legitimately be running a
// slow script long past the grace.
func TestASweepDoesNotReapARunOnAnOnlineDevice(t *testing.T) {
	f := newRunFixture(t, []string{"d1"})

	if _, err := f.db.Exec(
		`UPDATE scheduled_task_device_runs SET started_at = ? WHERE run_id = ?`,
		time.Now().UTC().Add(-24*time.Hour), f.runID); err != nil {
		t.Fatal(err)
	}

	if n, err := f.repo.AbandonOrphanedRuns(context.Background(), AbandonGrace); err != nil {
		t.Fatalf("sweep: %v", err)
	} else if n != 0 {
		t.Errorf("sweep reaped %d device runs on an online device, want 0", n)
	}
	if status, _ := f.parent(t); status != "running" {
		t.Errorf("parent status = %q, want \"running\": the agent is still connected", status)
	}
}

// TestAMixedRunClosesOnlyWhenEveryDeviceIsAccountedFor keeps the rollup's own
// contract intact under the sweep: one live device still executing means the
// parent is still running, even though the other row was just reaped.
func TestAMixedRunClosesOnlyWhenEveryDeviceIsAccountedFor(t *testing.T) {
	f := newRunFixture(t, []string{"d1", "d2"})
	f.strand(t, "d1", f.deviceRunFor(t, "d1"))

	if _, err := f.repo.AbandonOrphanedRuns(context.Background(), AbandonGrace); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if status, _ := f.parent(t); status != "running" {
		t.Errorf("parent status = %q with d2 still executing, want \"running\"", status)
	}
	if rec := f.report(t, "d2", f.deviceRunFor(t, "d2"), "success"); rec.Code != 200 {
		t.Fatalf("d2 -> %d", rec.Code)
	}
	if status, completedAt := f.parent(t); status != "completed" || completedAt == nil {
		t.Errorf("parent = %q completed_at=%v, want \"completed\" with a timestamp",
			status, completedAt)
	}
}

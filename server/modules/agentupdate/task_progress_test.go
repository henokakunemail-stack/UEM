package agentupdate

import (
	"context"
	"errors"
	"testing"
)

// taskID returns the single update task queued against a device.
func (f *queueFixture) taskID(t *testing.T, deviceID string) string {
	t.Helper()
	var id string
	if err := f.db.Get(&id,
		`SELECT id FROM device_update_tasks WHERE device_id = ?`, deviceID); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *queueFixture) status(t *testing.T, taskID string) string {
	t.Helper()
	var status string
	if err := f.db.Get(&status,
		`SELECT status FROM device_update_tasks WHERE id = ?`, taskID); err != nil {
		t.Fatal(err)
	}
	return status
}

// TestAFinishedTaskIsNotDrivenBackwards is the regression test for the terminal
// guard.
//
// RecordTaskProgress used to be an unconditional SET status = ?. A device whose
// socket dropped just after reporting 'success' would retry 'downloading', and
// the task the operator had already watched turn green would go yellow again
// with nothing to explain it. handleAgentReport writes the device's new
// agent_version on 'success', so the fleet inventory and the task row then
// disagreed: the console said the upgrade was running, the device table said it
// had finished. How long that lasted depended on how many times the agent
// retried.
func TestAFinishedTaskIsNotDrivenBackwards(t *testing.T) {
	f := newQueueFixture(t, true)
	if rec := f.dispatch(t, "dev-online", "2.0.0"); rec.Code != 200 {
		t.Fatalf("dispatch -> %d %s", rec.Code, rec.Body.String())
	}
	taskID := f.taskID(t, "dev-online")
	ctx := context.Background()

	if err := f.repo.RecordTaskProgress(ctx, taskID, "success", ""); err != nil {
		t.Fatalf("record success: %v", err)
	}

	// The retry the agent actually sends after a dropped socket.
	err := f.repo.RecordTaskProgress(ctx, taskID, "downloading", "")
	if !errors.Is(err, ErrTaskAlreadyFinished) {
		t.Fatalf("late 'downloading' report -> %v, want ErrTaskAlreadyFinished: the "+
			"task finished and a retry must not reopen it", err)
	}
	if got := f.status(t, taskID); got != "success" {
		t.Errorf("status = %q after a late report, want \"success\": a finished "+
			"update that reads as running is worse than no update at all", got)
	}
}

// TestAFailedTaskCanStillBeCorrectedToSuccess keeps the guard from becoming a
// one-way trap. The guard only blocks writes whose incoming status is
// nonterminal, so 'failed' followed by a genuine successful retry still lands
// -- otherwise an operator who fixed the cause would be stuck with a task no
// report could ever close.
func TestAFailedTaskCanStillBeCorrectedToSuccess(t *testing.T) {
	f := newQueueFixture(t, true)
	if rec := f.dispatch(t, "dev-online", "2.0.0"); rec.Code != 200 {
		t.Fatalf("dispatch -> %d %s", rec.Code, rec.Body.String())
	}
	taskID := f.taskID(t, "dev-online")

	if err := f.repo.RecordTaskProgress(context.Background(), taskID, "failed", "checksum mismatch"); err != nil {
		t.Fatalf("record failed: %v", err)
	}
	if err := f.repo.RecordTaskProgress(context.Background(), taskID, "success", ""); err != nil {
		t.Fatalf("correct a failed task to success -> %v, want nil: the terminal "+
			"guard must not make a finished task unclosable", err)
	}
	if got := f.status(t, taskID); got != "success" {
		t.Errorf("status = %q, want \"success\"", got)
	}
}

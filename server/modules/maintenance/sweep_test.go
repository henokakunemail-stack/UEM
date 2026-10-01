package maintenance

import (
	"context"
	"testing"
	"time"
)

// TestSweepReapsStrandedTaskAndClosesJob is the regression test for the row
// that sat at 'dispatched' from 01:32 for the rest of the installation: the
// agent never reported, so no terminal report ever reached recomputeJobCounters
// and the job stayed 'running' forever. The console polled it every 3s and its
// "Running Now" count never came down.
func TestSweepReapsStrandedTaskAndClosesJob(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)

	seedDevice(t, d, "dev1", "PC-1", "online")

	job := &Job{Name: "nightly", TaskType: TaskCleanupTemp, TargetType: string(TargetAll), CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	targets, err := repo.ResolveTargets(ctx, TargetAll, "")
	if err != nil {
		t.Fatalf("resolve targets: %v", err)
	}
	if err := repo.CreateTasksForJob(ctx, job.ID, TaskCleanupTemp, targets); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	tasks, err := repo.GetTasksForJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d", len(tasks))
	}
	if err := repo.MarkTaskDispatched(ctx, tasks[0].ID); err != nil {
		t.Fatalf("dispatch task: %v", err)
	}

	// The task was dispatched long enough ago to be past the grace window.
	// updated_at is written by MarkTaskDispatched, so it has to be moved
	// directly — there is no legitimate way to make it old.
	if _, err := d.Exec(`UPDATE maintenance_tasks SET updated_at = ?`,
		time.Now().UTC().Add(-AbandonGrace-time.Minute)); err != nil {
		t.Fatalf("age the task: %v", err)
	}

	// A live device must not be touched while within grace: this is what keeps
	// the sweep from reaping a slow disk_check on a healthy machine.
	n, err := repo.AbandonOrphanedTasks(ctx, time.Hour)
	if err != nil {
		t.Fatalf("sweep with wide grace: %v", err)
	}
	if n != 0 {
		t.Fatalf("reaped %d tasks inside the grace window, want 0", n)
	}

	n, err = repo.AbandonOrphanedTasks(ctx, AbandonGrace)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped %d tasks, want 1", n)
	}

	got, err := repo.GetTask(ctx, tasks[0].ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != TaskStatusFailed {
		t.Errorf("task status = %q, want %q", got.Status, TaskStatusFailed)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage == "" {
		t.Error("task has no error_message: the operator needs the reason")
	}
	if got.CompletedAt == nil {
		t.Error("task completed_at is nil: the console shows no end time")
	}

	// The job must close too. This is the whole point: the recount is the only
	// writer of job status, and nothing else ever calls it for a task that
	// never reports.
	rollup, err := repo.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if rollup.Status != JobStatusFailed {
		t.Errorf("job status = %q, want %q", rollup.Status, JobStatusFailed)
	}
	if rollup.Failed != 1 || rollup.Completed != 0 {
		t.Errorf("job counters = %+v, want failed=1 completed=0", rollup)
	}
	if rollup.CompletedAt == nil {
		t.Error("job completed_at is nil: the job would keep polling forever")
	}

	// A second sweep must be a no-op, not a second rollup.
	n, err = repo.AbandonOrphanedTasks(ctx, AbandonGrace)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("second sweep reaped %d tasks, want 0", n)
	}
}

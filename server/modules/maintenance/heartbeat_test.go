package maintenance

import (
	"context"
	"testing"
	"time"
)

// The agent's heartbeat exists because disk_check's ceiling (30 min) is now
// longer than this sweep's grace (20 min). A step silent for its whole duration
// is reaped mid-scan. So a heartbeat has to be accepted as proof of life — and
// the moment it touches anything else, it destroys the result it was sent to
// protect.
//
// The failure it is most able to cause is silent. bytes_freed is guarded by
// `bytes_freed = bytes_freed + CASE WHEN step <> ? THEN ? ELSE 0 END`, which
// reads the step column to ask "has this step already been counted?". A
// heartbeat that advanced the step would make the real report of the step it
// was heartbeating look like a replay, and the bytes that step freed would be
// dropped — with the sweep then reporting that it freed less than it did, and
// nothing in the console to say why the number is short.
func TestHeartbeatKeepsTheStepAliveAndChangesNothingElse(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)
	seedDevice(t, d, "dev-a", "PC-A", "online")

	job := &Job{Name: "scan", TaskType: TaskFullScan, TargetType: string(TargetAll), CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	targets, err := repo.ResolveTargets(ctx, TargetAll, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := repo.CreateTasksForJob(ctx, job.ID, TaskFullScan, targets); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	tasks, err := repo.GetTasksForJob(ctx, job.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d (%v)", len(tasks), err)
	}
	taskID := tasks[0].ID
	if err := repo.MarkTaskDispatched(ctx, taskID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// cleanup_temp reports and reports some bytes. The agent is now inside
	// disk_check, which is the long one.
	if err := repo.RecordStep(ctx, StepReport{
		TaskID: taskID, Step: TaskCleanupTemp, Status: TaskStatusRunning, BytesFreed: 100,
	}); err != nil {
		t.Fatalf("record cleanup_temp: %v", err)
	}

	// Backdate the row so "updated_at moved" is distinguishable from "updated_at
	// was already recent". Without this the assertion below would pass whether or
	// not the heartbeat wrote anything.
	past := time.Now().UTC().Add(-AbandonGrace - time.Minute)
	if _, err := d.Exec(`UPDATE maintenance_tasks SET updated_at = ? WHERE id = ?`,
		past, taskID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	// Now the heartbeat, mid-disk_check. No sweep runs in this test: reaping
	// would close the task, and a terminal task absorbs late non-terminal
	// reports, so the heartbeat would be silently dropped and the assertions
	// below would prove nothing. The reap-versus-heartbeat contrast is the
	// other test's job.
	if err := repo.RecordStep(ctx, StepReport{
		TaskID: taskID, Step: TaskDiskCheck, Status: TaskStatusRunning, Heartbeat: true,
	}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	got, err := repo.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Step != TaskCleanupTemp {
		t.Errorf("step = %q, want %q — a heartbeat advanced the visible step, which "+
			"makes disk_check's own report look like a replay",
			got.Step, TaskCleanupTemp)
	}
	if got.BytesFreed != 100 {
		t.Errorf("bytes_freed = %d, want 100 — the heartbeat moved the byte counter", got.BytesFreed)
	}
	if got.Status != TaskStatusRunning {
		t.Errorf("status = %q, want %q", got.Status, TaskStatusRunning)
	}
	if !got.UpdatedAt.After(past) {
		t.Errorf("updated_at = %v, want it past the backdated %v — this is the one "+
			"column a heartbeat exists to move", got.UpdatedAt, past)
	}

	// The byte is the one that matters: disk_check finishing must add its own on
	// top, not be absorbed as a replay of a step already counted.
	if err := repo.RecordStep(ctx, StepReport{
		TaskID: taskID, Step: TaskDiskCheck, Status: TaskStatusCompleted, BytesFreed: 7,
	}); err != nil {
		t.Fatalf("record disk_check: %v", err)
	}
	got, err = repo.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.BytesFreed != 107 {
		t.Errorf("bytes_freed = %d, want 107 — disk_check's own bytes were dropped", got.BytesFreed)
	}
	if got.Status != TaskStatusCompleted {
		t.Errorf("status = %q, want %q", got.Status, TaskStatusCompleted)
	}
}

// A heartbeat is only worth accepting if the sweep treats it as life. This is
// the pairing the whole change rests on: a ceiling above the grace is only safe
// because the step says it is alive, so the sweep must actually believe it.
func TestTheSweepBelievesAHeartbeat(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)
	seedDevice(t, d, "dev-a", "PC-A", "online")

	job := &Job{Name: "j", TaskType: TaskDiskCheck, TargetType: string(TargetAll), CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	targets, err := repo.ResolveTargets(ctx, TargetAll, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if err := repo.CreateTasksForJob(ctx, job.ID, TaskDiskCheck, targets); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	tasks, _ := repo.GetTasksForJob(ctx, job.ID)
	taskID := tasks[0].ID
	if err := repo.MarkTaskDispatched(ctx, taskID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Backdate, sweep without a heartbeat: the row is abandoned. This is the
	// failure the heartbeat was added to prevent, so it has to be real.
	past := time.Now().UTC().Add(-AbandonGrace - time.Minute)
	if _, err := d.Exec(`UPDATE maintenance_tasks SET updated_at = ? WHERE id = ?`,
		past, taskID); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if reaped, err := repo.AbandonOrphanedTasks(ctx, AbandonGrace); err != nil {
		t.Fatalf("sweep: %v", err)
	} else if reaped != 1 {
		t.Fatalf("reaped %d, want 1 — this test cannot show the heartbeat rescuing anything", reaped)
	}

	// A fresh task, backdated again, this time heartbeating. The whole point is
	// that the only difference between the two rows is the heartbeat.
	job2 := &Job{Name: "j2", TaskType: TaskDiskCheck, TargetType: string(TargetAll), CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job2); err != nil {
		t.Fatalf("create job 2: %v", err)
	}
	if err := repo.CreateTasksForJob(ctx, job2.ID, TaskDiskCheck, targets); err != nil {
		t.Fatalf("create tasks 2: %v", err)
	}
	tasks2, _ := repo.GetTasksForJob(ctx, job2.ID)
	task2 := tasks2[0].ID
	if err := repo.MarkTaskDispatched(ctx, task2); err != nil {
		t.Fatalf("dispatch 2: %v", err)
	}
	if _, err := d.Exec(`UPDATE maintenance_tasks SET updated_at = ? WHERE id = ?`,
		past, task2); err != nil {
		t.Fatalf("backdate 2: %v", err)
	}
	if err := repo.RecordStep(ctx, StepReport{
		TaskID: task2, Step: TaskDiskCheck, Status: TaskStatusRunning, Heartbeat: true,
	}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if reaped, err := repo.AbandonOrphanedTasks(ctx, AbandonGrace); err != nil {
		t.Fatalf("sweep 2: %v", err)
	} else if reaped != 0 {
		t.Errorf("the sweep reaped a task whose agent heartbeated %v ago — every "+
			"ceiling above the grace depends on this not happening", AbandonGrace)
	}
}

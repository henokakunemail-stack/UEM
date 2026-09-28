package maintenance

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/jmoiron/sqlx"
)

func newTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func seedDevice(t *testing.T, d *sqlx.DB, id, hostname, status string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := d.Exec(`
		INSERT INTO devices (id, hostname, os_name, status, enrolled_at,
		                     device_secret_hash, created_at, updated_at)
		VALUES (?, ?, 'windows', ?, ?, 'secret_hash', ?, ?)`,
		id, hostname, status, now, now, now)
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

func exitCode(n int) *int   { return &n }
func text(s string) *string { return &s }

// A task moving to a terminal state must move the job's counters with it, and a
// repeated report for the same task must not double-count. This is the whole
// reason the counters are recomputed instead of incremented.
func TestJobCountersRecomputedOnTerminalStep(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)

	seedDevice(t, d, "dev1", "PC-1", "online")
	seedDevice(t, d, "dev2", "PC-2", "offline")
	seedDevice(t, d, "dev3", "PC-3", "online")

	job := &Job{Name: "nightly", TaskType: TaskFullScan, TargetType: string(TargetAll), CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	targets, err := repo.ResolveTargets(ctx, TargetAll, "")
	if err != nil {
		t.Fatalf("resolve targets: %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("want 3 targets, got %d", len(targets))
	}
	if targets[0].Hostname != "PC-1" || !targets[0].Online || targets[1].Online {
		t.Fatalf("target snapshot wrong: %+v", targets)
	}

	if err := repo.CreateTasksForJob(ctx, job.ID, TaskFullScan, targets); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	tasks, err := repo.GetTasksForJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get tasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("want 3 tasks, got %d", len(tasks))
	}

	// The offline device is skipped at dispatch time; the other two complete.
	if err := repo.MarkTaskSkipped(ctx, tasks[1].ID); err != nil {
		t.Fatalf("skip task: %v", err)
	}
	// One report per step, which is what the agent actually sends: a non-final
	// step posts 'running' and the final step posts the terminal status. The
	// step name is the idempotency key, so a resend of a step already recorded
	// is a no-op and each new step's bytes add to the running total.
	for _, id := range []string{tasks[0].ID, tasks[2].ID} {
		if err := repo.MarkTaskDispatched(ctx, id); err != nil {
			t.Fatalf("dispatch task: %v", err)
		}
		if err := repo.RecordStep(ctx, StepReport{
			TaskID: id, Step: "cleanup_temp", Status: TaskStatusRunning,
		}); err != nil {
			t.Fatalf("running step: %v", err)
		}
		if err := repo.RecordStep(ctx, StepReport{
			TaskID: id, Step: "disk_check", Status: TaskStatusCompleted,
			ExitCode: exitCode(0), OutputLog: text("freed 10MB"), BytesFreed: 10,
		}); err != nil {
			t.Fatalf("completed step: %v", err)
		}
	}
	// A duplicate completion of the step the task already closed on must be
	// idempotent.
	if err := repo.RecordStep(ctx, StepReport{
		TaskID: tasks[0].ID, Step: "disk_check", Status: TaskStatusCompleted,
		ExitCode: exitCode(0), BytesFreed: 999,
	}); err != nil {
		t.Fatalf("duplicate step: %v", err)
	}

	got, err := repo.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.TotalTasks != 3 || got.Completed != 2 || got.Skipped != 1 || got.Failed != 0 || got.Dispatched != 2 {
		t.Fatalf("counters wrong: %+v", got)
	}
	if got.Status != JobStatusPartial {
		t.Fatalf("want partial (1 skipped of 3), got %q", got.Status)
	}
	if got.CompletedAt == nil {
		t.Fatal("want completed_at set once the job is terminal")
	}
	firstDone := *got.CompletedAt

	prog, err := repo.GetJobProgress(ctx, job.ID)
	if err != nil {
		t.Fatalf("job progress: %v", err)
	}
	if prog.Percent != 100 || prog.BytesFreed != 20 || prog.Remaining != 0 {
		t.Fatalf("progress wrong: %+v", prog)
	}

	// The duplicate report must not move completed_at forward.
	time.Sleep(2 * time.Millisecond)
	if err := repo.RecordStep(ctx, StepReport{
		TaskID: tasks[2].ID, Step: "disk_check", Status: TaskStatusCompleted,
		ExitCode: exitCode(0),
	}); err != nil {
		t.Fatalf("second duplicate: %v", err)
	}
	got, err = repo.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job again: %v", err)
	}
	if !got.CompletedAt.Equal(firstDone) {
		t.Fatalf("completed_at moved: %v -> %v", firstDone, *got.CompletedAt)
	}
	if got.Completed != 2 {
		t.Fatalf("duplicate double-counted: %+v", got)
	}
}

// A fully successful job closes as completed, and a job whose only outcome is
// offline devices is partial rather than "completed" — nothing actually ran.
func TestJobTerminalStatus(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name    string
		online  int
		offline int
		want    string
	}{
		{"all online", 2, 0, JobStatusCompleted},
		{"all offline", 0, 2, JobStatusPartial},
		{"mixed", 1, 1, JobStatusPartial},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDB(t)
			repo := NewRepository(d)

			var targets []DeviceTarget
			for i := 0; i < tc.online+tc.offline; i++ {
				id := "dev" + string(rune('a'+i))
				status := "offline"
				if i < tc.online {
					status = "online"
				}
				seedDevice(t, d, id, "PC-"+id, status)
				targets = append(targets, DeviceTarget{DeviceID: id, Hostname: "PC-" + id, Online: status == "online"})
			}

			job := &Job{Name: "j", TaskType: TaskCleanupTemp, TargetType: string(TargetAll), CreatedBy: "u1"}
			if err := repo.CreateJob(ctx, job); err != nil {
				t.Fatalf("create job: %v", err)
			}
			if err := repo.CreateTasksForJob(ctx, job.ID, TaskCleanupTemp, targets); err != nil {
				t.Fatalf("create tasks: %v", err)
			}
			tasks, err := repo.GetTasksForJob(ctx, job.ID)
			if err != nil {
				t.Fatalf("get tasks: %v", err)
			}
			for i, task := range tasks {
				if i < tc.online {
					if err := repo.RecordStep(ctx, StepReport{
						TaskID: task.ID, Step: TaskCleanupTemp, Status: TaskStatusCompleted, ExitCode: exitCode(0),
					}); err != nil {
						t.Fatalf("complete: %v", err)
					}
				} else if err := repo.MarkTaskSkipped(ctx, task.ID); err != nil {
					t.Fatalf("skip: %v", err)
				}
			}

			got, err := repo.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatalf("get job: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got.Status)
			}
		})
	}
}

// Every slice read is what the console marshals straight to JSON: nil becomes
// null, and null.map(...) is a React crash.
func TestEmptyListsMarshalAsArray(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)

	jobs, total, err := repo.ListJobs(ctx, JobFilter{Status: "running"})
	if err != nil || total != 0 || jobs == nil || len(jobs) != 0 {
		t.Fatalf("ListJobs: %v %d %#v", err, total, jobs)
	}
	tasks, total, err := repo.ListTasks(ctx, TaskFilter{DeviceID: "nope"})
	if err != nil || total != 0 || tasks == nil || len(tasks) != 0 {
		t.Fatalf("ListTasks: %v %d %#v", err, total, tasks)
	}
	byJob, err := repo.GetTasksForJob(ctx, "nope")
	if err != nil || byJob == nil || len(byJob) != 0 {
		t.Fatalf("GetTasksForJob: %v %#v", err, byJob)
	}
	targets, err := repo.ResolveTargets(ctx, TargetGroup, "nope")
	if err != nil || targets == nil || len(targets) != 0 {
		t.Fatalf("ResolveTargets: %v %#v", err, targets)
	}
	if _, err := repo.ResolveTargets(ctx, "bogus", ""); err == nil {
		t.Fatal("want error for unsupported target_type")
	}
	if _, err := repo.GetJob(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := repo.GetTask(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := repo.RecordStep(ctx, StepReport{TaskID: "nope", Step: "s", Status: TaskStatusCompleted}); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := repo.RecordStep(ctx, StepReport{Step: "s", Status: TaskStatusCompleted}); err == nil {
		t.Fatal("want error for missing step")
	}
	if err := repo.RecordStep(ctx, StepReport{TaskID: "nope", Step: "s", Status: "weird"}); err == nil {
		t.Fatal("want error for invalid status")
	}
}

// Retired devices are not fleet members, so they must never be picked up.
func TestResolveTargetsExcludesRetired(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)

	now := time.Now().UTC()
	seedDevice(t, d, "live", "PC-LIVE", "online")
	_, err := d.Exec(`
		INSERT INTO devices (id, hostname, os_name, status, enrolled_at,
		                     device_secret_hash, created_at, updated_at, retired_at)
		VALUES ('gone', 'PC-GONE', 'windows', 'online', ?, 'secret_hash', ?, ?, ?)`,
		now, now, now, now)
	if err != nil {
		t.Fatalf("seed retired device: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO device_groups (id, name, created_at, updated_at) VALUES ('g1', 'lab', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO device_group_members (group_id, device_id, added_at) VALUES ('g1', 'live', ?), ('g1', 'gone', ?)`, now, now); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	all, err := repo.ResolveTargets(ctx, TargetAll, "")
	if err != nil || len(all) != 1 || all[0].DeviceID != "live" {
		t.Fatalf("all: %v %#v", err, all)
	}
	group, err := repo.ResolveTargets(ctx, TargetGroup, "g1")
	if err != nil || len(group) != 1 || group[0].DeviceID != "live" {
		t.Fatalf("group: %v %#v", err, group)
	}
	single, err := repo.ResolveTargets(ctx, TargetDevice, "gone")
	if err != nil || len(single) != 0 {
		t.Fatalf("retired device target must resolve empty: %v %#v", err, single)
	}
}

// A target selection on a 10k-device fleet crosses SQLite's variable limit if
// the INSERT is not chunked.
func TestBulkInsertChunks(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)

	const n = 3000
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		if _, err := tx.Exec(`
			INSERT INTO devices (id, hostname, os_name, status, enrolled_at,
			                     device_secret_hash, created_at, updated_at)
			VALUES (?, ?, 'windows', 'online', ?, 'secret_hash', ?, ?)`,
			"d"+string(rune(i)), "PC-"+string(rune(i)), now, now, now); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	job := &Job{Name: "fleet", TaskType: TaskCleanupTemp, TargetType: string(TargetAll), CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	targets, err := repo.ResolveTargets(ctx, TargetAll, "")
	if err != nil || len(targets) != n {
		t.Fatalf("resolve: %v %d", err, len(targets))
	}
	if err := repo.CreateTasksForJob(ctx, job.ID, TaskCleanupTemp, targets); err != nil {
		t.Fatalf("bulk insert: %v", err)
	}
	var got int
	if err := d.Get(&got, `SELECT COUNT(*) FROM maintenance_tasks WHERE job_id = ?`, job.ID); err != nil || got != n {
		t.Fatalf("task count: %d %v", got, err)
	}
	var total int
	if err := d.Get(&total, `SELECT total_tasks FROM maintenance_jobs WHERE id = ?`, job.ID); err != nil || total != n {
		t.Fatalf("job total: %d %v", total, err)
	}
}

// A full_scan posts four reports, one per step. The step name is the
// idempotency key, so every step must be visible and its bytes must add up
// instead of the first report winning and the rest being absorbed.
func TestFullScanStepsAccumulate(t *testing.T) {
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

	steps := []struct {
		step   string
		status string
		bytes  int64
	}{
		{TaskCleanupTemp, TaskStatusRunning, 100},
		{TaskMemoryHygiene, TaskStatusRunning, 20},
		{TaskLogMaintenance, TaskStatusRunning, 30},
		{TaskDiskCheck, TaskStatusCompleted, 0},
	}
	for _, s := range steps {
		if err := repo.RecordStep(ctx, StepReport{
			TaskID: taskID, Step: s.step, Status: s.status, BytesFreed: s.bytes,
		}); err != nil {
			t.Fatalf("record %s: %v", s.step, err)
		}
	}

	task, err := repo.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Step != TaskDiskCheck {
		t.Fatalf("step = %q, want %q — steps 2-4 were absorbed", task.Step, TaskDiskCheck)
	}
	if task.Status != TaskStatusCompleted {
		t.Fatalf("status = %q, want %q", task.Status, TaskStatusCompleted)
	}
	if task.BytesFreed != 150 {
		t.Fatalf("bytes_freed = %d, want 150 (sum of the three freeing steps)", task.BytesFreed)
	}

	// A resend of the final step must not double-count.
	if err := repo.RecordStep(ctx, StepReport{
		TaskID: taskID, Step: TaskDiskCheck, Status: TaskStatusCompleted, BytesFreed: 999,
	}); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	task, _ = repo.GetTask(ctx, taskID)
	if task.BytesFreed != 150 {
		t.Fatalf("duplicate double-counted: %d", task.BytesFreed)
	}
}

// A job whose target resolves to nothing must still close, or the history list
// shows a run stuck on 'running' for a job that never had work.
func TestZeroTargetJobCloses(t *testing.T) {
	ctx := context.Background()
	repo := NewRepository(newTestDB(t))
	job := &Job{Name: "empty", TaskType: TaskCleanupTemp, TargetType: string(TargetGroup), TargetID: "g1", CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := repo.CreateTasksForJob(ctx, job.ID, TaskCleanupTemp, []DeviceTarget{}); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	got, err := repo.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.Status == JobStatusRunning {
		t.Fatalf("job still running with total_tasks=%d — a zero-target run never closes", got.TotalTasks)
	}
	if got.CompletedAt == nil {
		t.Fatal("want completed_at set on a closed job")
	}
}

// job.dispatched reads 0 for the whole in-flight window unless the dispatch
// path recounts, and a dispatch of an unknown task must say so rather than
// report success.
func TestDispatchUpdatesCountersAndRejectsUnknown(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)
	seedDevice(t, d, "dev-a", "PC-A", "online")
	seedDevice(t, d, "dev-b", "PC-B", "online")

	job := &Job{Name: "j", TaskType: TaskCleanupTemp, TargetType: string(TargetAll), CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	targets, _ := repo.ResolveTargets(ctx, TargetAll, "")
	if err := repo.CreateTasksForJob(ctx, job.ID, TaskCleanupTemp, targets); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	tasks, _ := repo.GetTasksForJob(ctx, job.ID)
	for _, task := range tasks {
		if err := repo.MarkTaskDispatched(ctx, task.ID); err != nil {
			t.Fatalf("dispatch %s: %v", task.ID, err)
		}
	}
	got, _ := repo.GetJob(ctx, job.ID)
	if got.Dispatched != 2 {
		t.Fatalf("job.dispatched = %d, want 2 — nothing recomputes on the dispatch path", got.Dispatched)
	}
	if err := repo.MarkTaskDispatched(ctx, "no-such-task"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkTaskDispatched on an unknown task = %v, want ErrNotFound", err)
	}
	// A repeat dispatch is a no-op, not an error and not a double count.
	if err := repo.MarkTaskDispatched(ctx, tasks[0].ID); err != nil {
		t.Fatalf("repeat dispatch: %v", err)
	}
	got, _ = repo.GetJob(ctx, job.ID)
	if got.Dispatched != 2 {
		t.Fatalf("repeat dispatch changed dispatched to %d", got.Dispatched)
	}
}

// A report may not walk a running task back to pending or dispatched: either
// would re-arm the MarkTaskDispatched guard and get the same task dispatched
// twice mid-run.
func TestStepReportCannotRegressStatus(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)
	seedDevice(t, d, "dev-a", "PC-A", "online")

	job := &Job{Name: "j", TaskType: TaskCleanupTemp, TargetType: string(TargetAll), CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	targets, _ := repo.ResolveTargets(ctx, TargetAll, "")
	if err := repo.CreateTasksForJob(ctx, job.ID, TaskCleanupTemp, targets); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	tasks, _ := repo.GetTasksForJob(ctx, job.ID)
	id := tasks[0].ID
	if err := repo.MarkTaskDispatched(ctx, id); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := repo.RecordStep(ctx, StepReport{
		TaskID: id, Step: TaskCleanupTemp, Status: TaskStatusRunning,
	}); err != nil {
		t.Fatalf("running step: %v", err)
	}

	if err := repo.RecordStep(ctx, StepReport{
		TaskID: id, Step: TaskCleanupTemp, Status: TaskStatusPending,
	}); err == nil {
		t.Fatal("a 'pending' report was accepted on a running task — it re-arms the dispatch guard")
	}
	if err := repo.RecordStep(ctx, StepReport{
		TaskID: id, Step: TaskCleanupTemp, Status: TaskStatusDispatched,
	}); err == nil {
		t.Fatal("a 'dispatched' report was accepted on a running task")
	}
	task, _ := repo.GetTask(ctx, id)
	if task.Status != TaskStatusRunning {
		t.Fatalf("status regressed to %q", task.Status)
	}
}

// Repro A: replaying an EARLIER step (not the current one) must not re-add its
// bytes. The single-step lookback only remembers the most recent step, so a
// replay of cleanup_temp after memory_hygiene slipped through and inflated
// bytes_freed while rewinding the visible step.
func TestReplayOfEarlierStepIsNoOp(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)
	seedDevice(t, d, "dev-a", "PC-A", "online")

	job := &Job{Name: "scan", TaskType: TaskFullScan, TargetType: string(TargetAll), CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	targets, _ := repo.ResolveTargets(ctx, TargetAll, "")
	if err := repo.CreateTasksForJob(ctx, job.ID, TaskFullScan, targets); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	tasks, _ := repo.GetTasksForJob(ctx, job.ID)
	id := tasks[0].ID
	if err := repo.MarkTaskDispatched(ctx, id); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	for _, s := range []struct {
		step  string
		bytes int64
	}{{TaskCleanupTemp, 100}, {TaskMemoryHygiene, 20}} {
		if err := repo.RecordStep(ctx, StepReport{
			TaskID: id, Step: s.step, Status: TaskStatusRunning, BytesFreed: s.bytes,
		}); err != nil {
			t.Fatalf("record %s: %v", s.step, err)
		}
	}

	// Replay the FIRST step. bytes must stay 120 and the visible step must stay
	// memory_hygiene.
	if err := repo.RecordStep(ctx, StepReport{
		TaskID: id, Step: TaskCleanupTemp, Status: TaskStatusRunning, BytesFreed: 100,
	}); err != nil {
		t.Fatalf("replay earlier step: %v", err)
	}
	got, err := repo.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.BytesFreed != 120 {
		t.Errorf("bytes_freed = %d, want 120 — a replayed earlier step was re-counted", got.BytesFreed)
	}
	if got.Step != TaskMemoryHygiene {
		t.Errorf("step = %q, want %q — a replayed earlier step rewound the visible step", got.Step, TaskMemoryHygiene)
	}
}

// Repro B: an agent must not be able to post 'skipped'. It is server-set by
// MarkTaskSkipped for an offline device; an online agent posting it would be
// recorded as a benign offline skip.
func TestAgentCannotPostSkipped(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)
	seedDevice(t, d, "dev-a", "PC-A", "online")

	job := &Job{Name: "j", TaskType: TaskCleanupTemp, TargetType: string(TargetAll), CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	targets, _ := repo.ResolveTargets(ctx, TargetAll, "")
	if err := repo.CreateTasksForJob(ctx, job.ID, TaskCleanupTemp, targets); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	tasks, _ := repo.GetTasksForJob(ctx, job.ID)
	id := tasks[0].ID
	if err := repo.MarkTaskDispatched(ctx, id); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if err := repo.RecordStep(ctx, StepReport{
		TaskID: id, Step: TaskCleanupTemp, Status: TaskStatusSkipped,
	}); err == nil {
		t.Error("RecordStep accepted status=skipped from an agent report")
	}
	got, _ := repo.GetTask(ctx, id)
	if got.Status == TaskStatusSkipped {
		t.Error("task was marked skipped by an agent report")
	}
}

// Repro C: a reboot requirement raised by an EARLY step must survive to the
// terminal step. The non-terminal UPDATE omitted reboot_required, so the latch
// never engaged for a requirement detected before the final step.
func TestRebootRequiredLatchesFromEarlyStep(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	repo := NewRepository(d)
	seedDevice(t, d, "dev-a", "PC-A", "online")

	job := &Job{Name: "scan", TaskType: TaskFullScan, TargetType: string(TargetAll), CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	targets, _ := repo.ResolveTargets(ctx, TargetAll, "")
	if err := repo.CreateTasksForJob(ctx, job.ID, TaskFullScan, targets); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	tasks, _ := repo.GetTasksForJob(ctx, job.ID)
	id := tasks[0].ID
	if err := repo.MarkTaskDispatched(ctx, id); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Early step raises the reboot need, terminal step does not.
	if err := repo.RecordStep(ctx, StepReport{
		TaskID: id, Step: TaskCleanupTemp, Status: TaskStatusRunning, RebootRequired: true,
	}); err != nil {
		t.Fatalf("record early step: %v", err)
	}
	if err := repo.RecordStep(ctx, StepReport{
		TaskID: id, Step: TaskDiskCheck, Status: TaskStatusCompleted, RebootRequired: false,
	}); err != nil {
		t.Fatalf("record terminal step: %v", err)
	}
	got, _ := repo.GetTask(ctx, id)
	if !got.RebootRequired {
		t.Error("reboot_required was lost — the non-terminal branch did not latch it")
	}
}

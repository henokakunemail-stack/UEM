package maintenance

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jmoiron/sqlx"
)

var ErrNotFound = errors.New("maintenance record not found")

const (
	jobCols = `id, name, task_type, target_type, target_id, created_by,
		total_tasks, dispatched, skipped, completed, failed, status, started_at, completed_at`

	taskCols = `id, job_id, device_id, hostname, task_type, status, step,
		exit_code, output_log, error_message, reboot_required, bytes_freed,
		started_at, completed_at, created_at, updated_at`
)

// taskInsertChunk keeps a bulk INSERT under SQLite's variable limit
// (999 on older builds, 8 columns per row) so a 10k-device "all" target works
// on every driver build.
const taskInsertChunk = 100

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// DeviceTarget is one resolved maintenance target. Hostname is copied onto the
// task row at creation so history still reads correctly after a device is
// renamed or retired; Online is the status snapshot the dispatch loop needs to
// decide between sending a task and skipping it.
type DeviceTarget struct {
	DeviceID string `db:"device_id"`
	Hostname string `db:"hostname"`
	Online   bool   `db:"online"`
}

type JobFilter struct {
	Status     string
	TaskType   string
	TargetType string
	Limit      int
	Offset     int
}

type TaskFilter struct {
	JobID    string
	DeviceID string
	Status   string
	TaskType string
	Limit    int
	Offset   int
}

// JobProgress is the console's poll-while-running read. The job counters come
// from maintenance_jobs (kept correct by recomputeJobCounters) and the
// aggregate columns come straight from the task rows. Remaining is the count of
// tasks that are not terminal yet — pending, dispatched and running together —
// which is what a progress bar needs; Dispatched counts tasks that actually
// reached an agent, so a skipped-offline device is never counted as one.
type JobProgress struct {
	JobID          string     `json:"job_id" db:"job_id"`
	Status         string     `json:"status" db:"status"`
	TotalTasks     int        `json:"total_tasks" db:"total_tasks"`
	Remaining      int        `json:"remaining" db:"remaining"`
	Dispatched     int        `json:"dispatched" db:"dispatched"`
	Skipped        int        `json:"skipped" db:"skipped"`
	Completed      int        `json:"completed" db:"completed"`
	Failed         int        `json:"failed" db:"failed"`
	BytesFreed     int64      `json:"bytes_freed" db:"bytes_freed"`
	RebootRequired int        `json:"reboot_required" db:"reboot_required"`
	Percent        float64    `json:"percent" db:"percent"`
	CompletedAt    *time.Time `json:"completed_at" db:"completed_at"`
}

// --- Target resolution ---

// ResolveTargets expands a RunRequest target into concrete devices. Retired
// devices are never fleet members, so they are excluded on every branch.
func (r *Repository) ResolveTargets(ctx context.Context, targetType TargetType, targetID string) ([]DeviceTarget, error) {
	const base = `
		SELECT d.id AS device_id,
		       COALESCE(d.hostname, '') AS hostname,
		       CASE WHEN d.status = 'online' THEN 1 ELSE 0 END AS online
		FROM devices d`

	targets := []DeviceTarget{}
	var err error
	switch targetType {
	case TargetDevice:
		err = r.db.SelectContext(ctx, &targets, base+`
			WHERE d.id = ? AND d.retired_at IS NULL`, targetID)
	case TargetGroup:
		err = r.db.SelectContext(ctx, &targets, base+`
			JOIN device_group_members m ON m.device_id = d.id
			WHERE m.group_id = ? AND d.retired_at IS NULL`, targetID)
	case TargetAll:
		err = r.db.SelectContext(ctx, &targets, base+`
			WHERE d.retired_at IS NULL
			ORDER BY d.hostname ASC`)
	default:
		return nil, fmt.Errorf("unsupported target_type: %q", targetType)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve maintenance targets: %w", err)
	}
	return targets, nil
}

// --- Jobs ---

// CreateJob inserts a running job. Counters start at zero and are derived from
// the task rows by recomputeJobCounters, never incremented by the caller.
func (r *Repository) CreateJob(ctx context.Context, job *Job) error {
	if job.ID == "" {
		job.ID = newID()
	}
	job.StartedAt = time.Now().UTC()
	job.Status = JobStatusRunning

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO maintenance_jobs (
			id, name, task_type, target_type, target_id, created_by,
			total_tasks, dispatched, skipped, completed, failed, status, started_at
		) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, 0, 0, ?, ?)`,
		job.ID, job.Name, job.TaskType, job.TargetType, job.TargetID,
		job.CreatedBy, job.Status, job.StartedAt)
	if err != nil {
		return fmt.Errorf("create maintenance job: %w", err)
	}
	return nil
}

func (r *Repository) GetJob(ctx context.Context, id string) (*Job, error) {
	var job Job
	err := r.db.GetContext(ctx, &job, `SELECT `+jobCols+` FROM maintenance_jobs WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get maintenance job %s: %w", id, err)
	}
	return &job, nil
}

// ListJobs returns one page of jobs plus the total count matching the filters.
func (r *Repository) ListJobs(ctx context.Context, f JobFilter) ([]Job, int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	where := ` WHERE 1=1`
	args := []any{}
	if f.Status != "" {
		where += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.TaskType != "" {
		where += ` AND task_type = ?`
		args = append(args, f.TaskType)
	}
	if f.TargetType != "" {
		where += ` AND target_type = ?`
		args = append(args, f.TargetType)
	}

	var total int
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM maintenance_jobs`+where, args...); err != nil {
		return nil, 0, fmt.Errorf("count maintenance jobs: %w", err)
	}

	q := `SELECT ` + jobCols + ` FROM maintenance_jobs` + where + ` ORDER BY started_at DESC LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), f.Limit, f.Offset)
	jobs := []Job{}
	if err := r.db.SelectContext(ctx, &jobs, q, pageArgs...); err != nil {
		return nil, 0, fmt.Errorf("list maintenance jobs: %w", err)
	}
	return jobs, total, nil
}

// GetJobProgress is the console's running-job read.
func (r *Repository) GetJobProgress(ctx context.Context, jobID string) (*JobProgress, error) {
	var p JobProgress
	q := `
		SELECT j.id AS job_id, j.status AS status, j.total_tasks AS total_tasks,
		       j.dispatched AS dispatched, j.skipped AS skipped,
		       j.completed AS completed, j.failed AS failed,
		       j.completed_at AS completed_at,
		       COALESCE(SUM(CASE WHEN t.status IN ('pending', 'dispatched', 'running') THEN 1 ELSE 0 END), 0) AS remaining,
		       COALESCE(SUM(t.bytes_freed), 0) AS bytes_freed,
		       COALESCE(SUM(CASE WHEN t.reboot_required = 1 THEN 1 ELSE 0 END), 0) AS reboot_required
		FROM maintenance_jobs j
		LEFT JOIN maintenance_tasks t ON t.job_id = j.id
		WHERE j.id = ?
		GROUP BY j.id, j.status, j.total_tasks, j.dispatched, j.skipped,
		         j.completed, j.failed, j.completed_at`
	if err := r.db.GetContext(ctx, &p, q, jobID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get maintenance job progress %s: %w", jobID, err)
	}
	if p.TotalTasks > 0 {
		finished := p.Completed + p.Failed + p.Skipped
		p.Percent = math.Round((float64(finished)/float64(p.TotalTasks))*1000) / 10
	}
	return &p, nil
}

// --- Tasks ---

// CreateTasksForJob inserts one pending task per resolved target and sets the
// job's total_tasks in the same transaction. Tasks are created pending even for
// offline devices: whether a device is reachable is decided at dispatch time,
// when the socket table is the freshest answer, and an offline target is then
// flipped to skipped by MarkTaskSkipped.
func (r *Repository) CreateTasksForJob(ctx context.Context, jobID, taskType string, targets []DeviceTarget) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if len(targets) == 0 {
		// A target that resolves to nothing must not leave the job reading
		// 'running' forever. With zero task rows the recount falls through to
		// JobStatusCompleted, which is the honest reading: nothing was left to
		// do.
		if err := recomputeJobCounters(ctx, tx, jobID); err != nil {
			return err
		}
		return tx.Commit()
	}

	now := time.Now().UTC()
	rows := make([]Task, 0, len(targets))
	for _, tgt := range targets {
		rows = append(rows, Task{
			ID:        newID(),
			JobID:     jobID,
			DeviceID:  tgt.DeviceID,
			Hostname:  tgt.Hostname,
			TaskType:  taskType,
			Status:    TaskStatusPending,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}

	const insertPrefix = `
		INSERT INTO maintenance_tasks (
			id, job_id, device_id, hostname, task_type, status,
			created_at, updated_at
		) VALUES `

	for start := 0; start < len(rows); start += taskInsertChunk {
		end := start + taskInsertChunk
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[start:end]

		args := make([]any, 0, len(batch)*8)
		values := ""
		for i, t := range batch {
			if i > 0 {
				values += ","
			}
			values += "(?, ?, ?, ?, ?, ?, ?, ?)"
			args = append(args, t.ID, t.JobID, t.DeviceID, t.Hostname,
				t.TaskType, t.Status, t.CreatedAt, t.UpdatedAt)
		}
		if _, err := tx.ExecContext(ctx, insertPrefix+values, args...); err != nil {
			return fmt.Errorf("insert maintenance tasks: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE maintenance_jobs SET total_tasks = ? WHERE id = ?`, len(rows), jobID); err != nil {
		return fmt.Errorf("set maintenance job total_tasks: %w", err)
	}
	return tx.Commit()
}

func (r *Repository) GetTask(ctx context.Context, id string) (*Task, error) {
	var t Task
	err := r.db.GetContext(ctx, &t, `SELECT `+taskCols+` FROM maintenance_tasks WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get maintenance task %s: %w", id, err)
	}
	return &t, nil
}

// GetTasksForJob returns every task in a job, hostname-ordered. Jobs are bounded
// by the fleet size, so this is deliberately unpaged — the console renders the
// whole device list of one run.
func (r *Repository) GetTasksForJob(ctx context.Context, jobID string) ([]Task, error) {
	tasks := []Task{}
	err := r.db.SelectContext(ctx, &tasks, `
		SELECT `+taskCols+`
		FROM maintenance_tasks
		WHERE job_id = ?
		ORDER BY hostname ASC`, jobID)
	if err != nil {
		return nil, fmt.Errorf("list maintenance tasks for job %s: %w", jobID, err)
	}
	return tasks, nil
}

// ListTasks returns one page of tasks plus the total count matching the filters.
func (r *Repository) ListTasks(ctx context.Context, f TaskFilter) ([]Task, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	where := ` WHERE 1=1`
	args := []any{}
	if f.JobID != "" {
		where += ` AND job_id = ?`
		args = append(args, f.JobID)
	}
	if f.DeviceID != "" {
		where += ` AND device_id = ?`
		args = append(args, f.DeviceID)
	}
	if f.Status != "" {
		where += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.TaskType != "" {
		where += ` AND task_type = ?`
		args = append(args, f.TaskType)
	}

	var total int
	if err := r.db.GetContext(ctx, &total,
		`SELECT COUNT(*) FROM maintenance_tasks`+where, args...); err != nil {
		return nil, 0, fmt.Errorf("count maintenance tasks: %w", err)
	}

	q := `SELECT ` + taskCols + ` FROM maintenance_tasks` + where + ` ORDER BY created_at DESC, hostname ASC LIMIT ? OFFSET ?`
	pageArgs := append(append([]any{}, args...), f.Limit, f.Offset)
	tasks := []Task{}
	if err := r.db.SelectContext(ctx, &tasks, q, pageArgs...); err != nil {
		return nil, 0, fmt.Errorf("list maintenance tasks: %w", err)
	}
	return tasks, total, nil
}

// MarkTaskDispatched records that the command reached the agent. The
// status = 'pending' guard makes a repeated dispatch a no-op, and started_at is
// only set once so a late ack cannot move it. The job recount is what makes
// job.dispatched non-zero for the whole in-flight window, which is the only
// time the console can show a sweep actually reaching machines.
func (r *Repository) MarkTaskDispatched(ctx context.Context, taskID string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The lookup is what removes the silent-success problem: a bare UPDATE on an
	// unknown id reports success, and a caller that dispatched nothing would see
	// its counters stall with nothing to explain it.
	var jobID string
	if err := tx.GetContext(ctx, &jobID,
		`SELECT job_id FROM maintenance_tasks WHERE id = ?`, taskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("get maintenance task %s: %w", taskID, err)
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE maintenance_tasks
		SET status = ?, started_at = COALESCE(started_at, ?), updated_at = ?
		WHERE id = ? AND status = ?`,
		TaskStatusDispatched, now, now, taskID, TaskStatusPending); err != nil {
		return fmt.Errorf("mark maintenance task dispatched %s: %w", taskID, err)
	}

	// A task already past 'pending' zero-rows the guarded UPDATE but still
	// recounts: the recount reads the same state, so it is idempotent.
	if err := recomputeJobCounters(ctx, tx, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkTaskSkipped closes a task that was never sent because the device had no
// live connection, then recomputes the job counters so the console's skip
// count and job status move together.
func (r *Repository) MarkTaskSkipped(ctx context.Context, taskID string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var jobID string
	if err := tx.GetContext(ctx, &jobID,
		`SELECT job_id FROM maintenance_tasks WHERE id = ?`, taskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("get maintenance task %s: %w", taskID, err)
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE maintenance_tasks
		SET status = ?, error_message = COALESCE(error_message, 'device offline'),
		    completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`,
		TaskStatusSkipped, now, now, taskID, TaskStatusPending); err != nil {
		return fmt.Errorf("skip maintenance task %s: %w", taskID, err)
	}

	if err := recomputeJobCounters(ctx, tx, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

// Bounds on the agent-supplied strings. These land in a TEXT column and are
// rendered verbatim in the console, so an agent that ships a whole log file (or
// a confused one that ships megabytes) must not be able to bloat the row.
const (
	maxStepLen      = 64
	maxOutputLogLen = 8 * 1024
	maxErrorMsgLen  = 2 * 1024
)

// RecordStep writes one agent step report. `step` is the idempotency key: a
// resend of the step the task already recorded is a no-op, and a new step
// accumulates its bytes_freed on top of the previous ones. That is what makes
// full_scan's four steps both visible and additive instead of the first one
// winning and the rest being absorbed by a terminal latch.
func (r *Repository) RecordStep(ctx context.Context, rep StepReport) error {
	if rep.Step == "" {
		return errors.New("step is required")
	}
	if !isTaskStatus(rep.Status) {
		return fmt.Errorf("invalid step status: %q", rep.Status)
	}
	// 'skipped' is server-set, by MarkTaskSkipped, for a target that was
	// offline. An agent that posts it is reporting on a device that answered,
	// so accepting it would record a live sweep as a benign offline skip and
	// drop it out of the failed count. MarkTaskSkipped is the only writer.
	if rep.Status == TaskStatusSkipped {
		return errors.New("status 'skipped' is set by the server, not an agent report")
	}
	rep.Step = truncate(rep.Step, maxStepLen)
	rep.OutputLog = truncatePtr(rep.OutputLog, maxOutputLogLen)
	rep.ErrorMessage = truncatePtr(rep.ErrorMessage, maxErrorMsgLen)

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var task struct {
		JobID    string `db:"job_id"`
		Status   string `db:"status"`
		Step     string `db:"step"`
		TaskType string `db:"task_type"`
	}
	if err := tx.GetContext(ctx, &task,
		`SELECT job_id, status, step, task_type FROM maintenance_tasks WHERE id = ?`, rep.TaskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("get maintenance task %s: %w", rep.TaskID, err)
	}

	// A report may not move the task backwards through its own step sequence.
	// The task row stores only the CURRENT step, so comparing against that
	// column alone makes idempotency a one-step lookback: a replay of any
	// earlier step would pass, re-add its bytes_freed, and rewind the visible
	// step. Comparing against the task type's canonical order makes a replay
	// (or an out-of-order report) a no-op instead of a silent corruption, and
	// it needs no per-step ledger row to do it.
	newIdx := StepIndex(task.TaskType, rep.Step)
	if newIdx < 0 {
		return fmt.Errorf("step %q does not belong to task type %q", rep.Step, task.TaskType)
	}
	if task.Step != "" {
		curIdx := StepIndex(task.TaskType, task.Step)
		if curIdx >= 0 && newIdx < curIdx {
			// Replay of an already-recorded step. Absorb it: the bytes were
			// counted the first time and must not be counted twice.
			return nil
		}
	}

	now := time.Now().UTC()

	if !isTerminalTaskStatus(rep.Status) {
		// An agent only ever reports 'running'. Admitting 'pending' or
		// 'dispatched' here would let a replayed report re-arm the
		// MarkTaskDispatched guard and get the same task dispatched twice
		// mid-run.
		if rep.Status != TaskStatusRunning {
			return fmt.Errorf("non-terminal step must be %q, got %q", TaskStatusRunning, rep.Status)
		}
		// A terminal task absorbs late non-terminal reports: a resend of step
		// 3's 'running' must not reopen a task that already finished step 4.
		// (Replay of an earlier step is already absorbed by the step-order
		// check above, before this point.)
		if isTerminalTaskStatus(task.Status) {
			return nil
		}
		// A reboot requirement raised by an early step of full_scan must
		// survive to the terminal step, so this latches exactly as the
		// terminal branch does.
		rebootRequired := 0
		if rep.RebootRequired {
			rebootRequired = 1
		}
		// A heartbeat is a liveness signal and nothing else. It must not touch
		// step, exit_code, output_log, error_message or bytes_freed, and the
		// reason is the byte counter's idempotency guard below: that guard
		// reads the step column to decide "has this step already been
		// counted?". A heartbeat that advanced the step would make the real
		// report of the step it was heartbeating look like a replay, and the
		// bytes that step freed would be dropped — silently, on a sweep that
		// then reported having freed less than it did. So a heartbeat only
		// moves updated_at, which is the one column the sweep reads.
		if rep.Heartbeat {
			if _, err := tx.ExecContext(ctx, `
				UPDATE maintenance_tasks SET updated_at = ? WHERE id = ?`,
				now, rep.TaskID); err != nil {
				return fmt.Errorf("record heartbeat on %s: %w", rep.TaskID, err)
			}
			return tx.Commit()
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE maintenance_tasks
			SET status = ?, step = ?, exit_code = ?, output_log = ?, error_message = ?,
			    reboot_required = CASE WHEN ? = 1 THEN 1 ELSE reboot_required END,
			    bytes_freed = bytes_freed + CASE WHEN step <> ? THEN ? ELSE 0 END,
			    started_at = COALESCE(started_at, ?), updated_at = ?
			WHERE id = ?`,
			rep.Status, rep.Step, rep.ExitCode, rep.OutputLog, rep.ErrorMessage,
			rebootRequired, rep.Step, rep.BytesFreed, now, now, rep.TaskID); err != nil {
			return fmt.Errorf("record step on %s: %w", rep.TaskID, err)
		}
		return tx.Commit()
	}

	// Terminal branch. A replay of an EARLIER step was absorbed by the
	// step-order check above. What is left is a resend of the step the task
	// already closed on, which must also be a no-op, and a genuinely new
	// terminal step, which must land.
	if isTerminalTaskStatus(task.Status) && task.Step == rep.Step {
		return nil
	}

	rebootRequired := 0
	if rep.RebootRequired {
		rebootRequired = 1
	}
	// reboot_required latches to 1 and never un-requires. completed_at keeps the
	// first terminal transition, so a duplicate cannot push the end time.
	if _, err := tx.ExecContext(ctx, `
		UPDATE maintenance_tasks
		SET status = ?, step = ?, exit_code = ?, output_log = ?, error_message = ?,
		    reboot_required = CASE WHEN ? = 1 THEN 1 ELSE reboot_required END,
		    bytes_freed = bytes_freed + CASE WHEN step <> ? THEN ? ELSE 0 END,
		    completed_at = COALESCE(completed_at, ?), updated_at = ?
		WHERE id = ?`,
		rep.Status, rep.Step, rep.ExitCode, rep.OutputLog, rep.ErrorMessage,
		rebootRequired, rep.Step, rep.BytesFreed, now, now, rep.TaskID); err != nil {
		return fmt.Errorf("record terminal step on %s: %w", rep.TaskID, err)
	}
	if err := recomputeJobCounters(ctx, tx, task.JobID); err != nil {
		return err
	}
	return tx.Commit()
}

// truncate byte-slices. A multi-byte rune can be cut at the boundary, but these
// strings are terminal text rendered inside a <pre>, not markup, so a trailing
// replacement byte is cosmetic and not worth a rune-slice copy on every report.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func truncatePtr(s *string, max int) *string {
	if s == nil {
		return nil
	}
	t := truncate(*s, max)
	return &t
}

func isTerminalTaskStatus(status string) bool {
	return status == TaskStatusCompleted || status == TaskStatusFailed || status == TaskStatusSkipped
}

func isTaskStatus(status string) bool {
	switch status {
	case TaskStatusPending, TaskStatusDispatched, TaskStatusRunning,
		TaskStatusCompleted, TaskStatusFailed, TaskStatusSkipped:
		return true
	}
	return false
}

// recomputeJobCounters recomputes the job's tallies from its task rows and
// closes the job when nothing is in flight.
//
// Counting instead of incrementing is the whole point: an agent may retry a
// step report, a device may reconnect and get a second dispatch, and two
// concurrent completions can interleave. Every one of those moves the same
// task row, so a COUNT-based tally is idempotent where an increment would
// double-count and could close the job early.
func recomputeJobCounters(ctx context.Context, tx *sqlx.Tx, jobID string) error {
	var c struct {
		Total      int `db:"total"`
		Remaining  int `db:"remaining"`
		Dispatched int `db:"dispatched"`
		Skipped    int `db:"skipped"`
		Completed  int `db:"completed"`
		Failed     int `db:"failed"`
	}
	err := tx.GetContext(ctx, &c, `
		SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN status IN ('pending', 'dispatched', 'running') THEN 1 ELSE 0 END), 0) AS remaining,
			COALESCE(SUM(CASE WHEN status IN ('dispatched', 'running', 'completed', 'failed') THEN 1 ELSE 0 END), 0) AS dispatched,
			COALESCE(SUM(CASE WHEN status = 'skipped' THEN 1 ELSE 0 END), 0) AS skipped,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) AS completed,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) AS failed
		FROM maintenance_tasks
		WHERE job_id = ?`, jobID)
	if err != nil {
		return fmt.Errorf("count maintenance task statuses for job %s: %w", jobID, err)
	}

	status, completedAt := JobStatusRunning, (*time.Time)(nil)
	if c.Remaining == 0 {
		switch {
		case c.Total == 0, c.Failed == 0 && c.Completed == c.Total:
			// Nothing to do, or every task succeeded.
			status = JobStatusCompleted
		case c.Failed == c.Total:
			status = JobStatusFailed
		default:
			// Any mix — including an all-skipped run, where nothing actually
			// executed and "completed" would overstate what happened.
			status = JobStatusPartial
		}
		now := time.Now().UTC()
		completedAt = &now
	}

	// COALESCE on completed_at keeps the first terminal transition as the end
	// time; a duplicate report must not push it later.
	_, err = tx.ExecContext(ctx, `
		UPDATE maintenance_jobs
		SET total_tasks = ?, dispatched = ?, skipped = ?, completed = ?, failed = ?,
		    status = ?, completed_at = COALESCE(?, completed_at)
		WHERE id = ?`,
		c.Total, c.Dispatched, c.Skipped, c.Completed, c.Failed,
		status, completedAt, jobID)
	if err != nil {
		return fmt.Errorf("update maintenance job counters %s: %w", jobID, err)
	}
	return nil
}

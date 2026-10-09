package taskscheduler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
)

type ScriptTemplate struct {
	ID             string    `json:"id" db:"id"`
	Name           string    `json:"name" db:"name"`
	Description    string    `json:"description" db:"description"`
	ScriptType     string    `json:"script_type" db:"script_type"` // 'powershell', 'cmd', 'bash', 'sh'
	ScriptContent  string    `json:"script_content" db:"script_content"`
	SHA256Hash     string    `json:"sha256_hash" db:"sha256_hash"`
	DefaultArgs    string    `json:"default_args" db:"default_args"`
	TimeoutSeconds int       `json:"timeout_seconds" db:"timeout_seconds"`
	CreatedBy      string    `json:"created_by" db:"created_by"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" db:"updated_at"`
}

type TaskSchedule struct {
	ID           string     `json:"id" db:"id"`
	Name         string     `json:"name" db:"name"`
	Description  string     `json:"description" db:"description"`
	ScriptID     string     `json:"script_id" db:"script_id"`
	TargetType   string     `json:"target_type" db:"target_type"` // 'device', 'group', 'all'
	TargetID     string     `json:"target_id" db:"target_id"`
	ScheduleType string     `json:"schedule_type" db:"schedule_type"` // 'interval', 'cron', 'once'
	ScheduleExpr string     `json:"schedule_expr" db:"schedule_expr"`
	IsEnabled    bool       `json:"is_enabled" db:"is_enabled"`
	LastRunAt    *time.Time `json:"last_run_at" db:"last_run_at"`
	NextRunAt    *time.Time `json:"next_run_at" db:"next_run_at"`
	CreatedBy    string     `json:"created_by" db:"created_by"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at" db:"updated_at"`

	// Joined fields
	ScriptName string `json:"script_name,omitempty" db:"script_name"`
}

type ScheduledTaskRun struct {
	ID          string     `json:"id" db:"id"`
	ScheduleID  string     `json:"schedule_id" db:"schedule_id"`
	ScriptID    string     `json:"script_id" db:"script_id"`
	Status      string     `json:"status" db:"status"` // 'running', 'completed', 'failed'
	TriggeredAt time.Time  `json:"triggered_at" db:"triggered_at"`
	CompletedAt *time.Time `json:"completed_at" db:"completed_at"`

	ScheduleName string `json:"schedule_name,omitempty" db:"schedule_name"`
	ScriptName   string `json:"script_name,omitempty" db:"script_name"`
}

type ScheduledTaskDeviceRun struct {
	ID           string     `json:"id" db:"id"`
	RunID        string     `json:"run_id" db:"run_id"`
	DeviceID     string     `json:"device_id" db:"device_id"`
	Status       string     `json:"status" db:"status"` // 'pending', 'dispatched', 'success', 'failed'
	ExitCode     *int       `json:"exit_code" db:"exit_code"`
	OutputLog    *string    `json:"output_log" db:"output_log"`
	ErrorMessage *string    `json:"error_message" db:"error_message"`
	StartedAt    *time.Time `json:"started_at" db:"started_at"`
	CompletedAt  *time.Time `json:"completed_at" db:"completed_at"`

	Hostname string `json:"hostname,omitempty" db:"hostname"`
	Site     string `json:"site,omitempty" db:"site"`
}

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func CalculateSHA256(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// --- Script CRUD ---

func (r *Repository) ListScripts(ctx context.Context) ([]ScriptTemplate, error) {
	scripts := []ScriptTemplate{}
	err := r.db.SelectContext(ctx, &scripts, `
		SELECT id, name, description, script_type, script_content, sha256_hash,
		       default_args, timeout_seconds, created_by, created_at, updated_at
		FROM script_templates
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list scripts: %w", err)
	}
	return scripts, nil
}

func (r *Repository) GetScriptByID(ctx context.Context, id string) (*ScriptTemplate, error) {
	var s ScriptTemplate
	err := r.db.GetContext(ctx, &s, `
		SELECT id, name, description, script_type, script_content, sha256_hash,
		       default_args, timeout_seconds, created_by, created_at, updated_at
		FROM script_templates
		WHERE id = ?
	`, id)
	if err != nil {
		return nil, fmt.Errorf("get script: %w", err)
	}
	return &s, nil
}

func (r *Repository) CreateScript(ctx context.Context, s *ScriptTemplate) error {
	now := time.Now().UTC()
	if s.ID == "" {
		s.ID = NewID()
	}
	s.SHA256Hash = CalculateSHA256(s.ScriptContent)
	s.CreatedAt = now
	s.UpdatedAt = now
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = 300
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO script_templates (
			id, name, description, script_type, script_content, sha256_hash,
			default_args, timeout_seconds, created_by, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, s.ID, s.Name, s.Description, s.ScriptType, s.ScriptContent, s.SHA256Hash,
		s.DefaultArgs, s.TimeoutSeconds, s.CreatedBy, s.CreatedAt, s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create script: %w", err)
	}
	return nil
}

func (r *Repository) UpdateScript(ctx context.Context, s *ScriptTemplate) error {
	s.UpdatedAt = time.Now().UTC()
	s.SHA256Hash = CalculateSHA256(s.ScriptContent)
	if s.TimeoutSeconds <= 0 {
		s.TimeoutSeconds = 300
	}

	res, err := r.db.ExecContext(ctx, `
		UPDATE script_templates SET
			name = ?, description = ?, script_type = ?, script_content = ?,
			sha256_hash = ?, default_args = ?, timeout_seconds = ?, updated_at = ?
		WHERE id = ?
	`, s.Name, s.Description, s.ScriptType, s.ScriptContent,
		s.SHA256Hash, s.DefaultArgs, s.TimeoutSeconds, s.UpdatedAt, s.ID)
	if err != nil {
		return fmt.Errorf("update script: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("script not found")
	}
	return nil
}

func (r *Repository) DeleteScript(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM script_templates WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete script: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("script not found")
	}
	return nil
}

// --- Schedule CRUD ---

func (r *Repository) ListSchedules(ctx context.Context) ([]TaskSchedule, error) {
	schedules := []TaskSchedule{}
	query := `
		SELECT
			s.id, s.name, s.description, s.script_id, s.target_type, s.target_id,
			s.schedule_type, s.schedule_expr, s.is_enabled, s.last_run_at, s.next_run_at,
			s.created_by, s.created_at, s.updated_at,
			COALESCE(sc.name, '') as script_name
		FROM task_schedules s
		LEFT JOIN script_templates sc ON sc.id = s.script_id
		ORDER BY s.name ASC
	`
	if err := r.db.SelectContext(ctx, &schedules, query); err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	return schedules, nil
}

func (r *Repository) GetScheduleByID(ctx context.Context, id string) (*TaskSchedule, error) {
	var s TaskSchedule
	query := `
		SELECT
			s.id, s.name, s.description, s.script_id, s.target_type, s.target_id,
			s.schedule_type, s.schedule_expr, s.is_enabled, s.last_run_at, s.next_run_at,
			s.created_by, s.created_at, s.updated_at,
			COALESCE(sc.name, '') as script_name
		FROM task_schedules s
		LEFT JOIN script_templates sc ON sc.id = s.script_id
		WHERE s.id = ?
	`
	if err := r.db.GetContext(ctx, &s, query, id); err != nil {
		return nil, fmt.Errorf("get schedule: %w", err)
	}
	return &s, nil
}

// nextRunAt is when an interval schedule will fire next, or nil when the
// schedule has no computable next fire.
//
// The column existed in the schema and was selected on every read, but nothing
// ever wrote it, so the console's "Next run" column showed an em dash for every
// schedule in the installation. The poller does not use it either -- it compares
// last_run_at against the interval -- so this is for the operator, not for
// scheduling, and a nil is an honest "not an interval schedule" rather than a
// bug.
//
// The expression is minutes as a plain integer. That is the only form
// isRunnableScheduleType admits, so there is no cron to compute here; add the
// parser when there is a cron to compute.
func nextRunAt(s *TaskSchedule, from time.Time) *time.Time {
	if !s.IsEnabled || s.ScheduleType != "interval" {
		return nil
	}
	minutes, err := strconv.Atoi(s.ScheduleExpr)
	if err != nil || minutes <= 0 {
		return nil
	}
	next := from.UTC().Add(time.Duration(minutes) * time.Minute)
	return &next
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (r *Repository) CreateSchedule(ctx context.Context, s *TaskSchedule) error {
	now := time.Now().UTC()
	if s.ID == "" {
		s.ID = NewID()
	}
	s.CreatedAt = now
	s.UpdatedAt = now
	s.NextRunAt = nextRunAt(s, now)

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO task_schedules (
			id, name, description, script_id, target_type, target_id,
			schedule_type, schedule_expr, is_enabled, next_run_at, created_by,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, s.ID, s.Name, s.Description, s.ScriptID, s.TargetType, s.TargetID,
		s.ScheduleType, s.ScheduleExpr, boolToInt(s.IsEnabled), s.NextRunAt, s.CreatedBy,
		s.CreatedAt, s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create schedule: %w", err)
	}
	return nil
}

func (r *Repository) UpdateSchedule(ctx context.Context, s *TaskSchedule) error {
	s.UpdatedAt = time.Now().UTC()
	// Recomputed from the new expression, not carried over: changing a schedule
	// from every 60 minutes to every 5 has to move the column, and turning it
	// off has to clear it.
	s.NextRunAt = nextRunAt(s, s.UpdatedAt)

	res, err := r.db.ExecContext(ctx, `
		UPDATE task_schedules SET
			name = ?, description = ?, script_id = ?, target_type = ?, target_id = ?,
			schedule_type = ?, schedule_expr = ?, is_enabled = ?, next_run_at = ?,
			updated_at = ?
		WHERE id = ?
	`, s.Name, s.Description, s.ScriptID, s.TargetType, s.TargetID,
		s.ScheduleType, s.ScheduleExpr, boolToInt(s.IsEnabled), s.NextRunAt, s.UpdatedAt, s.ID)
	if err != nil {
		return fmt.Errorf("update schedule: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("schedule not found")
	}
	return nil
}

func (r *Repository) DeleteSchedule(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM task_schedules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("schedule not found")
	}
	return nil
}

// --- Target Resolution ---

func (r *Repository) GetTargetDeviceIDs(ctx context.Context, targetType, targetID string) ([]string, error) {
	var ids []string
	switch targetType {
	case "device":
		var count int
		_ = r.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM devices WHERE id = ? AND retired_at IS NULL`, targetID)
		if count > 0 {
			ids = append(ids, targetID)
		}
	case "group":
		err := r.db.SelectContext(ctx, &ids, `
			SELECT device_id FROM device_group_members WHERE group_id = ?
		`, targetID)
		if err != nil {
			return nil, fmt.Errorf("select group devices: %w", err)
		}
	case "all":
		err := r.db.SelectContext(ctx, &ids, `SELECT id FROM devices WHERE retired_at IS NULL`)
		if err != nil {
			return nil, fmt.Errorf("select all devices: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported target_type: %s", targetType)
	}
	return ids, nil
}

// --- Run Tracking ---

// CreateRun records a new run of the schedule and moves the schedule's own
// run timestamps forward.
//
// next_run_at moves with last_run_at because leaving it would promise a fire
// time that has already passed: a schedule created at 09:00 for every 60
// minutes would still read "10:00" after the 10:00 run dispatched, while the
// real next one was 11:00. The poller reads neither column, so nothing would
// break -- the operator would simply be told a stale time, which is worse than
// the em dash it replaced.
//
// The schedule is passed in rather than looked up: the caller already resolved
// it to dispatch the run, and re-reading it here would be a second query on a
// path that already read it.
func (r *Repository) CreateRun(ctx context.Context, schedule *TaskSchedule, scriptID string) (*ScheduledTaskRun, error) {
	now := time.Now().UTC()
	runID := NewID()

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO scheduled_task_runs (id, schedule_id, script_id, status, triggered_at)
		VALUES (?, ?, ?, 'running', ?)
	`, runID, schedule.ID, scriptID, now)
	if err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}

	if _, err := r.db.ExecContext(ctx, `
		UPDATE task_schedules SET last_run_at = ?, next_run_at = ? WHERE id = ?`,
		now, nextRunAt(schedule, now), schedule.ID); err != nil {
		return nil, fmt.Errorf("record schedule run time: %w", err)
	}

	return r.GetRunByID(ctx, runID)
}

func (r *Repository) CreateDeviceRun(ctx context.Context, runID, deviceID string) (*ScheduledTaskDeviceRun, error) {
	id := NewID()
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO scheduled_task_device_runs (id, run_id, device_id, status, started_at)
		VALUES (?, ?, ?, 'dispatched', ?)
	`, id, runID, deviceID, now)
	if err != nil {
		return nil, fmt.Errorf("create device run: %w", err)
	}

	return &ScheduledTaskDeviceRun{
		ID:        id,
		RunID:     runID,
		DeviceID:  deviceID,
		Status:    "dispatched",
		StartedAt: &now,
	}, nil
}

func (r *Repository) UpdateDeviceRunResult(ctx context.Context, id, status string, exitCode int, output, errMsg string) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE scheduled_task_device_runs SET
			status = ?, exit_code = ?, output_log = ?, error_message = ?, completed_at = ?
		WHERE id = ?
	`, status, exitCode, output, errMsg, now, id)
	return err
}

// DeviceRunOwner returns the device a scheduled task device run belongs to.
//
// The agent result route needs it to check the run against the identity that
// was authenticated. The run id arrives in the URL, so it is as
// attacker-controlled as a device id in a path; an agent that authenticated as
// itself could otherwise write a result for anyone's run.
func (r *Repository) DeviceRunOwner(ctx context.Context, deviceRunID string) (string, error) {
	var deviceID string
	err := r.db.GetContext(ctx, &deviceID,
		`SELECT device_id FROM scheduled_task_device_runs WHERE id = ?`, deviceRunID)
	return deviceID, err
}

// SyncRunStatus recomputes a parent run's status from its device runs and
// writes it when every device run has reached a terminal state.
//
// This is the scheduled-task counterpart of the rollup that
// software-deployment already had, and it was missing in the same way. The
// parent was only ever completed by CompleteRun, which TriggerSchedule calls on
// exactly one branch: when the target resolved to zero devices. Once a run had
// devices, the parent was written as 'running' by CreateRun and nothing ever
// moved it -- not the agent result handler, not the scheduler poll. So a script
// dispatched to five endpoints that all reported success left the operator
// looking at a run stuck in 'running' with a NULL completed_at, forever, next
// to five green device rows.
//
// The status reflects what actually happened, not merely that everything ended:
// an all-failed run is 'failed', which is what an operator has to act on and
// what the console renders differently.
func (r *Repository) SyncRunStatus(ctx context.Context, deviceRunID string) error {
	var runID string
	if err := r.db.GetContext(ctx, &runID,
		`SELECT run_id FROM scheduled_task_device_runs WHERE id = ?`, deviceRunID); err != nil {
		return err
	}
	return r.rollupRunByID(ctx, runID)
}

// rollupRunByID is the rollup itself, keyed by parent, so the orphan sweeper
// can call it directly: a reaped device run has to be rolled up like a reported
// one, and by then the only id it has left is the parent's.
func (r *Repository) rollupRunByID(ctx context.Context, runID string) error {
	var counts struct {
		Total     int `db:"total"`
		Done      int `db:"done"`
		Succeeded int `db:"succeeded"`
	}
	if err := r.db.GetContext(ctx, &counts, `
		SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN status IN ('success', 'failed') THEN 1 ELSE 0 END), 0) AS done,
			COALESCE(SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END), 0) AS succeeded
		FROM scheduled_task_device_runs
		WHERE run_id = ?`, runID); err != nil {
		return err
	}

	// Not all device runs are terminal yet. Leaving the parent in 'running' is
	// the honest state: it is what the row means, and a device that never
	// reports is exactly what the operator needs to see.
	if counts.Total == 0 || counts.Total != counts.Done {
		return nil
	}

	status := "completed"
	if counts.Succeeded == 0 {
		status = "failed"
	}
	return r.CompleteRun(ctx, runID, status)
}

func (r *Repository) CompleteRun(ctx context.Context, runID, status string) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		UPDATE scheduled_task_runs SET status = ?, completed_at = ? WHERE id = ?
	`, status, now, runID)
	return err
}

func (r *Repository) ListRuns(ctx context.Context, scheduleID string, limit int) ([]ScheduledTaskRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	runs := []ScheduledTaskRun{}
	query := `
		SELECT
			r.id, r.schedule_id, r.script_id, r.status, r.triggered_at, r.completed_at,
			COALESCE(s.name, '') as schedule_name,
			COALESCE(sc.name, '') as script_name
		FROM scheduled_task_runs r
		LEFT JOIN task_schedules s ON s.id = r.schedule_id
		LEFT JOIN script_templates sc ON sc.id = r.script_id
		WHERE 1=1
	`
	var args []any
	if scheduleID != "" {
		query += " AND r.schedule_id = ?"
		args = append(args, scheduleID)
	}
	query += " ORDER BY r.triggered_at DESC LIMIT ?"
	args = append(args, limit)

	if err := r.db.SelectContext(ctx, &runs, query, args...); err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	return runs, nil
}

func (r *Repository) GetRunByID(ctx context.Context, id string) (*ScheduledTaskRun, error) {
	var run ScheduledTaskRun
	query := `
		SELECT
			r.id, r.schedule_id, r.script_id, r.status, r.triggered_at, r.completed_at,
			COALESCE(s.name, '') as schedule_name,
			COALESCE(sc.name, '') as script_name
		FROM scheduled_task_runs r
		LEFT JOIN task_schedules s ON s.id = r.schedule_id
		LEFT JOIN script_templates sc ON sc.id = r.script_id
		WHERE r.id = ?
	`
	if err := r.db.GetContext(ctx, &run, query, id); err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	return &run, nil
}

func (r *Repository) ListDeviceRuns(ctx context.Context, runID string) ([]ScheduledTaskDeviceRun, error) {
	runs := []ScheduledTaskDeviceRun{}
	query := `
		SELECT
			dr.id, dr.run_id, dr.device_id, dr.status, dr.exit_code, dr.output_log,
			dr.error_message, dr.started_at, dr.completed_at,
			COALESCE(d.hostname, '') as hostname,
			COALESCE(d.site, '') as site
		FROM scheduled_task_device_runs dr
		LEFT JOIN devices d ON d.id = dr.device_id
		WHERE dr.run_id = ?
		ORDER BY d.hostname ASC
	`
	if err := r.db.SelectContext(ctx, &runs, query, runID); err != nil {
		return nil, fmt.Errorf("list device runs: %w", err)
	}
	return runs, nil
}

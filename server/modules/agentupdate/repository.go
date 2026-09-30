package agentupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jmoiron/sqlx"
)

// ErrAlreadyDispatched means a task was already sent, so the caller lost the
// race with another reconnect. It is not a failure: the work is done.
var ErrAlreadyDispatched = errors.New("update task already dispatched")

// ErrTaskAlreadyFinished means the row already reached a terminal status, so
// a nonterminal report cannot move it. It is deliberately a separate value
// from ErrAlreadyDispatched: that one says the work was already handed to the
// device, this one says the work already finished. Callers that special-case
// the first -- SendTask treats it as a success -- would otherwise report a
// rejected status write as a completed send.
var ErrTaskAlreadyFinished = errors.New("update task already reached a final status")

type AgentRelease struct {
	ID             string    `db:"id" json:"id"`
	Version        string    `db:"version" json:"version"`
	OSName         string    `db:"os_name" json:"os_name"`
	Arch           string    `db:"arch" json:"arch"`
	FilePath       string    `db:"file_path" json:"file_path"`
	FileSize       int64     `db:"file_size" json:"file_size"`
	SHA256Checksum string    `db:"sha256_checksum" json:"sha256_checksum"`
	Changelog      string    `db:"changelog" json:"changelog"`
	IsActive       bool      `db:"is_active" json:"is_active"`
	UploadedBy     string    `db:"uploaded_by" json:"uploaded_by"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
}

// AgentReleaseDTO is the console-facing shape of a release. It is separate from
// AgentRelease on purpose: FilePath is a server-side absolute path that
// http.ServeFile needs, but shipping it to the browser would leak the server's
// directory layout for no benefit. The console only ever needs the name.
type AgentReleaseDTO struct {
	ID             string    `json:"id"`
	Version        string    `json:"version"`
	OSName         string    `json:"os_name"`
	Arch           string    `json:"arch"`
	FileName       string    `json:"file_name"`
	FileSize       int64     `json:"file_size"`
	SHA256Checksum string    `json:"sha256_checksum"`
	Changelog      string    `json:"changelog"`
	IsActive       bool      `json:"is_active"`
	UploadedBy     string    `json:"uploaded_by"`
	CreatedAt      time.Time `json:"created_at"`
}

func (a AgentRelease) toDTO() AgentReleaseDTO {
	return AgentReleaseDTO{
		ID:             a.ID,
		Version:        a.Version,
		OSName:         a.OSName,
		Arch:           a.Arch,
		FileName:       filepath.Base(a.FilePath),
		FileSize:       a.FileSize,
		SHA256Checksum: a.SHA256Checksum,
		Changelog:      a.Changelog,
		IsActive:       a.IsActive,
		UploadedBy:     a.UploadedBy,
		CreatedAt:      a.CreatedAt,
	}
}

type UpdateCampaign struct {
	ID                 string    `db:"id" json:"id"`
	Name               string    `db:"name" json:"name"`
	Description        string    `db:"description" json:"description"`
	TargetVersion      string    `db:"target_version" json:"target_version"`
	TargetType         string    `db:"target_type" json:"target_type"`
	TargetID           string    `db:"target_id" json:"target_id"`
	BatchSize          int       `db:"batch_size" json:"batch_size"`
	StaggerIntervalSec int       `db:"stagger_interval_sec" json:"stagger_interval_sec"`
	Status             string    `db:"status" json:"status"`
	CreatedBy          string    `db:"created_by" json:"created_by"`
	CreatedAt          time.Time `db:"created_at" json:"created_at"`
	UpdatedAt          time.Time `db:"updated_at" json:"updated_at"`
}

type DeviceUpdateTask struct {
	ID            string     `db:"id" json:"id"`
	CampaignID    *string    `db:"campaign_id" json:"campaign_id,omitempty"`
	DeviceID      string     `db:"device_id" json:"device_id"`
	FromVersion   string     `db:"from_version" json:"from_version"`
	TargetVersion string     `db:"target_version" json:"target_version"`
	Status        string     `db:"status" json:"status"`
	ErrorMessage  string     `db:"error_message" json:"error_message"`
	DispatchedAt  *time.Time `db:"dispatched_at" json:"dispatched_at,omitempty"`
	CompletedAt   *time.Time `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt     time.Time  `db:"created_at" json:"created_at"`
}

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

func (r *Repository) CreateRelease(ctx context.Context, release *AgentRelease) error {
	if release.ID == "" {
		release.ID = newID()
	}
	now := time.Now().UTC()
	release.CreatedAt = now

	query := `
		INSERT INTO agent_releases (
			id, version, os_name, arch, file_path, file_size, sha256_checksum,
			changelog, is_active, uploaded_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		release.ID, release.Version, release.OSName, release.Arch, release.FilePath,
		release.FileSize, release.SHA256Checksum, release.Changelog, release.IsActive,
		release.UploadedBy, release.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create release: %w", err)
	}
	return nil
}

func (r *Repository) GetRelease(ctx context.Context, id string) (*AgentRelease, error) {
	var rel AgentRelease
	err := r.db.GetContext(ctx, &rel, `SELECT * FROM agent_releases WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("get release %s: %w", id, err)
	}
	return &rel, nil
}

func (r *Repository) GetActiveReleaseForDevice(ctx context.Context, targetVersion, osName, arch string) (*AgentRelease, error) {
	var rel AgentRelease
	query := `
		SELECT * FROM agent_releases
		WHERE version = ? AND os_name = ? AND arch = ? AND is_active = 1
		LIMIT 1
	`
	err := r.db.GetContext(ctx, &rel, query, targetVersion, osName, arch)
	if err != nil {
		return nil, fmt.Errorf("get release for %s/%s/%s: %w", targetVersion, osName, arch, err)
	}
	return &rel, nil
}

func (r *Repository) ListReleases(ctx context.Context) ([]*AgentRelease, error) {
	list := []*AgentRelease{}
	err := r.db.SelectContext(ctx, &list, `SELECT * FROM agent_releases ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list releases: %w", err)
	}
	return list, nil
}

func (r *Repository) CreateCampaign(ctx context.Context, c *UpdateCampaign) error {
	if c.ID == "" {
		c.ID = newID()
	}
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	if c.Status == "" {
		c.Status = "draft"
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 20
	}
	if c.StaggerIntervalSec <= 0 {
		c.StaggerIntervalSec = 30
	}

	query := `
		INSERT INTO update_campaigns (
			id, name, description, target_version, target_type, target_id,
			batch_size, stagger_interval_sec, status, created_by, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		c.ID, c.Name, c.Description, c.TargetVersion, c.TargetType, c.TargetID,
		c.BatchSize, c.StaggerIntervalSec, c.Status, c.CreatedBy, c.CreatedAt, c.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create campaign: %w", err)
	}
	return nil
}

func (r *Repository) GetCampaign(ctx context.Context, id string) (*UpdateCampaign, error) {
	var c UpdateCampaign
	err := r.db.GetContext(ctx, &c, `SELECT * FROM update_campaigns WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("get campaign %s: %w", id, err)
	}
	return &c, nil
}

func (r *Repository) ListCampaigns(ctx context.Context) ([]*UpdateCampaign, error) {
	list := []*UpdateCampaign{}
	err := r.db.SelectContext(ctx, &list, `SELECT * FROM update_campaigns ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	return list, nil
}

func (r *Repository) UpdateCampaignStatus(ctx context.Context, id string, status string) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `UPDATE update_campaigns SET status = ?, updated_at = ? WHERE id = ?`, status, now, id)
	return err
}

func (r *Repository) CreateUpdateTask(ctx context.Context, t *DeviceUpdateTask) error {
	if t.ID == "" {
		t.ID = newID()
	}
	now := time.Now().UTC()
	t.CreatedAt = now
	if t.Status == "" {
		t.Status = "pending"
	}

	query := `
		INSERT INTO device_update_tasks (
			id, campaign_id, device_id, from_version, target_version,
			status, error_message, dispatched_at, completed_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		t.ID, t.CampaignID, t.DeviceID, t.FromVersion, t.TargetVersion,
		t.Status, t.ErrorMessage, t.DispatchedAt, t.CompletedAt, t.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create update task: %w", err)
	}
	return nil
}

func (r *Repository) GetUpdateTask(ctx context.Context, id string) (*DeviceUpdateTask, error) {
	var t DeviceUpdateTask
	err := r.db.GetContext(ctx, &t, `SELECT * FROM device_update_tasks WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("get task %s: %w", id, err)
	}
	return &t, nil
}

func (r *Repository) GetLatestTaskForDevice(ctx context.Context, deviceID string) (*DeviceUpdateTask, error) {
	var t DeviceUpdateTask
	err := r.db.GetContext(ctx, &t, `
		SELECT * FROM device_update_tasks
		WHERE device_id = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("get latest task for %s: %w", deviceID, err)
	}
	return &t, nil
}

// PendingTasksFor returns the update tasks still waiting to be sent to a device.
//
// It exists because "queued" used to be a status word in an HTTP response and
// nothing more. handleDispatchDeviceUpdate inserted the row with status
// 'dispatched' and stamped dispatched_at before it checked whether the device
// was online, so an offline device got a row that claimed it had been sent. No
// code re-reads those rows -- update.apply is only built at the two dispatch
// sites, both behind hub.Online -- and the offline sweeper in
// software-deployment only sweeps deployment_tasks, not this table. The result
// was a task stuck in 'dispatched' forever: the upgrade silently never happened
// and nothing in the console or the database said so.
func (r *Repository) PendingTasksFor(ctx context.Context, deviceID string) ([]*DeviceUpdateTask, error) {
	var tasks []*DeviceUpdateTask
	err := r.db.SelectContext(ctx, &tasks, `
		SELECT * FROM device_update_tasks
		WHERE device_id = ? AND status = 'pending'
		ORDER BY created_at ASC`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("pending tasks for %s: %w", deviceID, err)
	}
	return tasks, nil
}

// MarkDispatched stamps a task as sent and moves it out of the pending set, so a
// reconnect cannot send the same command twice.
func (r *Repository) MarkDispatched(ctx context.Context, taskID string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE device_update_tasks
		SET status = 'dispatched', dispatched_at = ?, error_message = ''
		WHERE id = ? AND status = 'pending'`, now, taskID)
	if err != nil {
		return err
	}
	// 0 rows means another reconnect beat this one to it, or the agent already
	// reported progress. Either way the command has been sent once, not twice.
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAlreadyDispatched
	}
	return nil
}

// RequeueTask puts a dispatched-but-undelivered task back into the pending set.
// The dispatched_at stamp is cleared too, so a task that has never actually
// reached a device does not carry a send time that says otherwise.
func (r *Repository) RequeueTask(ctx context.Context, taskID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE device_update_tasks
		SET status = 'pending', dispatched_at = NULL
		WHERE id = ?`, taskID)
	return err
}

// TaskDeviceID returns the device an update task belongs to, so the agent
// report route can check the task against the device it authenticated as
// instead of trusting a task id that arrived in the request body.
func (r *Repository) TaskDeviceID(ctx context.Context, taskID string) (string, error) {
	var deviceID string
	err := r.db.GetContext(ctx, &deviceID,
		`SELECT device_id FROM device_update_tasks WHERE id = ?`, taskID)
	return deviceID, err
}

// RecordTaskProgress writes one status report from an agent.
//
// The terminal guard is the point. Without it this was an unconditional
// SET status = ?, so a report that arrived late or was retried after a
// dropped socket moved a finished task back to 'downloading' or 'verifying'.
// That is not cosmetic: handleAgentReport writes the device's new
// agent_version on 'success', so the fleet inventory kept claiming the new
// version while the console showed the update running again, and how long
// that lasted depended on how many times the agent retried.
//
// Rejecting the regress is also the honest answer to the agent: the update
// already finished, so telling it to continue would restart work the device
// has no reason to redo. ErrTaskAlreadyFinished is its own value rather than
// a reuse of ErrAlreadyDispatched because SendTask reads that one as
// "someone else got there first, the work is done" and returns nil, so a
// rejected status write would otherwise be logged as a successful send.
//
// A genuine terminal-to-terminal correction -- 'failed' followed by a
// successful retry reported as 'success' -- is still allowed, because the
// guard only blocks writes whose incoming status is nonterminal.
func (r *Repository) RecordTaskProgress(ctx context.Context, taskID, status, errMsg string) error {
	now := time.Now().UTC()
	var completedAt *time.Time
	terminal := status == "success" || status == "failed" || status == "rollback"
	if terminal {
		completedAt = &now
	}

	query := `
		UPDATE device_update_tasks
		SET status = ?, error_message = ?, completed_at = COALESCE(?, completed_at)
		WHERE id = ?
	`
	args := []any{status, errMsg, completedAt, taskID}
	if !terminal {
		// Only a nonterminal report can regress a finished task. Terminal
		// reports are left alone above so a later 'success' can still
		// correct a task that had failed.
		query += ` AND status NOT IN ('success', 'failed', 'rollback')`
	}
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 && !terminal {
		return ErrTaskAlreadyFinished
	}
	return nil
}

func (r *Repository) UpdateDeviceAgentVersion(ctx context.Context, deviceID, version string) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `UPDATE devices SET agent_version = ?, updated_at = ? WHERE id = ?`, version, now, deviceID)
	return err
}

// SyncCampaignStatus recomputes a rollout's status from its task rows and
// writes it once every task has reached a terminal state.
//
// handleStartCampaign set 'in_progress' and nothing ever moved it again. The
// agent update path wrote device_update_tasks.status and stopped there, so a
// campaign finished its work -- every endpoint on the target version -- and
// still read 'in_progress' for the life of the installation. The console's
// "Active Campaigns" KPI counts exactly that status, so a completed rollout
// kept inflating a number that was supposed to tell an operator how much work was
// outstanding, and the Start button stayed available on a campaign that had
// nothing left to dispatch.
//
// 'failed' when no task succeeded, matching the software-deployment and
// task-scheduler rollups: a rollout that upgraded nothing must not read as a
// success. A campaign with no tasks at all cannot be failed for that, so it
// stays 'in_progress' and the operator can start it again against a fleet that
// was offline at the time.
func (r *Repository) SyncCampaignStatus(ctx context.Context, taskID string) error {
	var campaignID *string
	if err := r.db.GetContext(ctx, &campaignID,
		`SELECT campaign_id FROM device_update_tasks WHERE id = ?`, taskID); err != nil {
		return err
	}
	if campaignID == nil || *campaignID == "" {
		// A one-off dispatch, not part of a rollout. Nothing to roll up.
		return nil
	}

	var counts struct {
		Total     int `db:"total"`
		Done      int `db:"done"`
		Succeeded int `db:"succeeded"`
	}
	if err := r.db.GetContext(ctx, &counts, `
		SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN status IN ('success', 'failed', 'rollback') THEN 1 ELSE 0 END), 0) AS done,
			COALESCE(SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END), 0) AS succeeded
		FROM device_update_tasks
		WHERE campaign_id = ?`, *campaignID); err != nil {
		return err
	}
	if counts.Total == 0 || counts.Total != counts.Done {
		return nil
	}

	status := "completed"
	if counts.Succeeded == 0 {
		status = "failed"
	}
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `UPDATE update_campaigns SET status = ?, updated_at = ? WHERE id = ?`,
		status, now, *campaignID)
	return err
}

func (r *Repository) ResolveTargetDevices(ctx context.Context, targetType, targetID string) ([]string, error) {
	switch targetType {
	case "all":
		var ids []string
		err := r.db.SelectContext(ctx, &ids, `SELECT id FROM devices WHERE status != 'retired'`)
		return ids, err
	case "device":
		return []string{targetID}, nil
	case "group":
		var ids []string
		err := r.db.SelectContext(ctx, &ids, `
			SELECT m.device_id
			FROM device_group_members m
			JOIN devices d ON d.id = m.device_id
			WHERE m.group_id = ? AND d.status != 'retired'
		`, targetID)
		return ids, err
	default:
		return nil, fmt.Errorf("unknown target type: %s", targetType)
	}
}

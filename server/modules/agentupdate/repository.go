package agentupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"
)

// ErrAlreadyDispatched means a task was already sent, so the caller lost the
// race with another reconnect. It is not a failure: the work is done.
var ErrAlreadyDispatched = errors.New("update task already dispatched")

// ErrReleaseNotFound means a release id named by a caller has no row.
var ErrReleaseNotFound = errors.New("agent release not found")

// ErrTaskAlreadyFinished means the row already reached a terminal status, so
// a nonterminal report cannot move it. It is deliberately a separate value
// from ErrAlreadyDispatched: that one says the work was already handed to the
// device, this one says the work already finished. Callers that special-case
// the first -- SendTask treats it as a success -- would otherwise report a
// rejected status write as a completed send.
var ErrTaskAlreadyFinished = errors.New("update task already reached a final status")

type AgentRelease struct {
	ID                      string     `db:"id" json:"id"`
	Version                 string     `db:"version" json:"version"`
	OSName                  string     `db:"os_name" json:"os_name"`
	Arch                    string     `db:"arch" json:"arch"`
	FilePath                string     `db:"file_path" json:"file_path"`
	FileSize                int64      `db:"file_size" json:"file_size"`
	SHA256Checksum          string     `db:"sha256_checksum" json:"sha256_checksum"`
	Changelog               string     `db:"changelog" json:"changelog"`
	IsActive                bool       `db:"is_active" json:"is_active"`
	UploadedBy              string     `db:"uploaded_by" json:"uploaded_by"`
	CreatedAt               time.Time  `db:"created_at" json:"created_at"`
	PublishedAt             *time.Time `db:"published_at" json:"published_at,omitempty"`
	MinimumSupportedVersion string     `db:"minimum_supported_version" json:"minimum_supported_version"`
	// Ed25519Signature is a base64 detached signature over the canonical
	// release manifest. Blank for releases uploaded before signing existed;
	// agents built with a trusted key refuse those rather than install them.
	Ed25519Signature string `db:"ed25519_signature" json:"ed25519_signature"`
	DownloadURL      string `db:"download_url" json:"download_url"`
}

// AgentReleaseDTO is the console-facing shape of a release. It is separate from
// AgentRelease on purpose: FilePath is a server-side absolute path that
// http.ServeFile needs, but shipping it to the browser would leak the server's
// directory layout for no benefit. The console only ever needs the name.
type AgentReleaseDTO struct {
	ID                      string     `json:"id"`
	Version                 string     `json:"version"`
	OSName                  string     `json:"os_name"`
	Arch                    string     `json:"arch"`
	FileName                string     `json:"file_name"`
	FileSize                int64      `json:"file_size"`
	SHA256Checksum          string     `json:"sha256_checksum"`
	Changelog               string     `json:"changelog"`
	IsActive                bool       `json:"is_active"`
	UploadedBy              string     `json:"uploaded_by"`
	CreatedAt               time.Time  `json:"created_at"`
	PublishedAt             *time.Time `json:"published_at,omitempty"`
	MinimumSupportedVersion string     `json:"minimum_supported_version"`
	Ed25519Signature        string     `json:"ed25519_signature"`
	DownloadURL             string     `json:"download_url"`
	// Signed reports whether a signature is on the row. The console shows it
	// because an unsigned release is installable only by agents built without
	// a trusted key, and an operator looking at the list needs to see which
	// rows those are.
	Signed bool `json:"signed"`
}

func (a AgentRelease) toDTO() AgentReleaseDTO {
	return AgentReleaseDTO{
		ID:                      a.ID,
		Version:                 a.Version,
		OSName:                  a.OSName,
		Arch:                    a.Arch,
		FileName:                filepath.Base(a.FilePath),
		FileSize:                a.FileSize,
		SHA256Checksum:          a.SHA256Checksum,
		Changelog:               a.Changelog,
		IsActive:                a.IsActive,
		UploadedBy:              a.UploadedBy,
		CreatedAt:               a.CreatedAt,
		PublishedAt:             a.PublishedAt,
		MinimumSupportedVersion: a.MinimumSupportedVersion,
		Ed25519Signature:        a.Ed25519Signature,
		DownloadURL:             a.DownloadURL,
		Signed:                  a.Ed25519Signature != "",
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
	// NextWaveAt is when the wave loop releases the next batch; nil when no
	// wave is pending.
	NextWaveAt *time.Time `db:"next_wave_at" json:"next_wave_at,omitempty"`
}

type UpdateCampaignDTO struct {
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
	TotalDevices       int       `db:"total_devices" json:"total_devices"`
	SucceededDevices   int       `db:"succeeded_devices" json:"succeeded_devices"`
	FailedDevices      int       `db:"failed_devices" json:"failed_devices"`
	PendingDevices     int       `db:"pending_devices" json:"pending_devices"`
	InProgressDevices  int       `db:"in_progress_devices" json:"in_progress_devices"`
	// NextWaveAt is when the next batch is released; nil when none is due.
	NextWaveAt *time.Time `db:"next_wave_at" json:"next_wave_at,omitempty"`
	// QueuedDevices are pending tasks their wave has released: they wait for
	// the device to come online. The rest of PendingDevices wait for a wave.
	QueuedDevices int `db:"queued_devices" json:"queued_devices"`
}

type CampaignTaskDetail struct {
	ID                 string     `db:"id" json:"id"`
	CampaignID         *string    `db:"campaign_id" json:"campaign_id,omitempty"`
	DeviceID           string     `db:"device_id" json:"device_id"`
	Hostname           string     `db:"hostname" json:"hostname"`
	OSName             string     `db:"os_name" json:"os_name"`
	DeviceOnlineStatus string     `db:"device_online_status" json:"device_online_status"`
	CurrentVersion     string     `db:"current_version" json:"current_version"`
	FromVersion        string     `db:"from_version" json:"from_version"`
	TargetVersion      string     `db:"target_version" json:"target_version"`
	Status             string     `db:"status" json:"status"`
	ErrorMessage       string     `db:"error_message" json:"error_message"`
	DispatchedAt       *time.Time `db:"dispatched_at" json:"dispatched_at,omitempty"`
	CompletedAt        *time.Time `db:"completed_at" json:"completed_at,omitempty"`
	CreatedAt          time.Time  `db:"created_at" json:"created_at"`
	// ReleasedAt tells "waiting for its wave" (nil) from "waiting for the
	// device" on a pending task.
	ReleasedAt *time.Time `db:"released_at" json:"released_at,omitempty"`
}

type CampaignLogItem struct {
	ID        string    `db:"id" json:"id"`
	Timestamp time.Time `db:"created_at" json:"timestamp"`
	ActorType string    `db:"actor_type" json:"actor_type"`
	ActorID   string    `db:"actor_id" json:"actor_id"`
	Action    string    `db:"action" json:"action"`
	TargetID  string    `db:"target_id" json:"target_id"`
	Details   string    `db:"details" json:"details"`
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
	// ReleasedAt is set when the task's wave let it go; a pending task without
	// it is still waiting for its wave, not for its device.
	ReleasedAt *time.Time `db:"released_at" json:"released_at,omitempty"`
	// ProgressAt is the last report the agent sent for this task.
	ProgressAt *time.Time `db:"progress_at" json:"progress_at,omitempty"`
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

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
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
			changelog, is_active, uploaded_by, created_at,
			published_at, minimum_supported_version, ed25519_signature, download_url
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		release.ID, release.Version, release.OSName, release.Arch, release.FilePath,
		release.FileSize, release.SHA256Checksum, release.Changelog, boolToInt(release.IsActive),
		release.UploadedBy, release.CreatedAt,
		release.PublishedAt, release.MinimumSupportedVersion, release.Ed25519Signature,
		release.DownloadURL,
	)
	if err != nil {
		return fmt.Errorf("create release: %w", err)
	}
	return nil
}

// SignRelease stores the signature and the fields the signature covers. The
// caller has already produced the signature off-box with a private key this
// server never holds; this write only records what the signer said.
//
// The columns move together because the signature covers exactly this set.
// Writing the signature without the floor it certifies, or the floor without
// the signature that certifies it, would leave a row whose parts disagree
// about what an agent is being asked to trust.
func (r *Repository) SignRelease(ctx context.Context, id, signature, minimumSupportedVersion, downloadURL string, publishedAt time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE agent_releases
		SET ed25519_signature = ?, minimum_supported_version = ?,
		    download_url = ?, published_at = ?
		WHERE id = ?`,
		signature, minimumSupportedVersion, downloadURL, publishedAt.UTC(), id)
	if err != nil {
		return fmt.Errorf("sign release %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrReleaseNotFound
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

// LatestActiveRelease is the newest active release for an OS and
// architecture, whatever its version: what a fresh install should get.
func (r *Repository) LatestActiveRelease(ctx context.Context, osName, arch string) (*AgentRelease, error) {
	var rel AgentRelease
	err := r.db.GetContext(ctx, &rel, r.db.Rebind(`
		SELECT * FROM agent_releases
		WHERE LOWER(os_name) = ? AND LOWER(arch) = ? AND is_active = 1
		ORDER BY created_at DESC
		LIMIT 1`), strings.ToLower(osName), strings.ToLower(arch))
	if err != nil {
		return nil, fmt.Errorf("latest release for %s/%s: %w", osName, arch, err)
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

func (r *Repository) ListCampaigns(ctx context.Context) ([]*UpdateCampaignDTO, error) {
	list := []*UpdateCampaignDTO{}
	query := `
		SELECT
			c.id, c.name, c.description, c.target_version, c.target_type, c.target_id,
			c.batch_size, c.stagger_interval_sec, c.status, c.created_by, c.created_at, c.updated_at,
			c.next_wave_at,
			COALESCE(counts.total_devices, 0) AS total_devices,
			COALESCE(counts.succeeded_devices, 0) AS succeeded_devices,
			COALESCE(counts.failed_devices, 0) AS failed_devices,
			COALESCE(counts.pending_devices, 0) AS pending_devices,
			COALESCE(counts.in_progress_devices, 0) AS in_progress_devices,
			COALESCE(counts.queued_devices, 0) AS queued_devices
		FROM update_campaigns c
		LEFT JOIN (
			SELECT
				campaign_id,
				COUNT(*) AS total_devices,
				SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) AS succeeded_devices,
				SUM(CASE WHEN status IN ('failed', 'rollback') THEN 1 ELSE 0 END) AS failed_devices,
				SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END) AS pending_devices,
				SUM(CASE WHEN status IN ('dispatched', 'downloading', 'verifying', 'swapping') THEN 1 ELSE 0 END) AS in_progress_devices,
				SUM(CASE WHEN status = 'pending' AND released_at IS NOT NULL THEN 1 ELSE 0 END) AS queued_devices
			FROM device_update_tasks
			WHERE campaign_id IS NOT NULL
			GROUP BY campaign_id
		) counts ON counts.campaign_id = c.id
		ORDER BY c.created_at DESC
	`
	err := r.db.SelectContext(ctx, &list, query)
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	return list, nil
}

// CancelCampaign cancels a campaign that has not finished and fails its open
// tasks, reporting whether it cancelled anything. Both writes are one
// transaction, and the tasks are touched only when the campaign itself was
// cancelled: a cancel that lost a race with completion used to fail the tasks
// of a campaign that had already finished.
func (r *Repository) CancelCampaign(ctx context.Context, campaignID string) (bool, error) {
	now := time.Now().UTC()
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("cancel campaign: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		UPDATE update_campaigns
		SET status = 'cancelled', updated_at = ?, next_wave_at = NULL
		WHERE id = ? AND status IN ('draft', 'pending', 'in_progress')
	`, now, campaignID)
	if err != nil {
		return false, fmt.Errorf("cancel campaign: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}

	const cancelMsg = "Campaign cancelled by administrator"
	if _, err := tx.ExecContext(ctx, `
		UPDATE device_update_tasks
		SET status = 'failed', error_message = ?, completed_at = ?
		WHERE campaign_id = ? AND status IN ('pending', 'dispatched', 'downloading', 'verifying', 'swapping')
	`, cancelMsg, now, campaignID); err != nil {
		return false, fmt.Errorf("cancel campaign tasks: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("cancel campaign: %w", err)
	}
	return true, nil
}

func (r *Repository) ListCampaignTasks(ctx context.Context, campaignID string) ([]*CampaignTaskDetail, error) {
	var list []*CampaignTaskDetail
	query := `
		SELECT
			t.id, t.campaign_id, t.device_id,
			COALESCE(d.hostname, t.device_id) AS hostname,
			COALESCE(d.os_name, 'unknown') AS os_name,
			COALESCE(d.status, 'offline') AS device_online_status,
			COALESCE(d.agent_version, t.from_version) AS current_version,
			t.from_version, t.target_version,
			t.status, t.error_message,
			t.dispatched_at, t.completed_at, t.created_at, t.released_at
		FROM device_update_tasks t
		LEFT JOIN devices d ON d.id = t.device_id
		WHERE t.campaign_id = ?
		ORDER BY t.created_at ASC
	`
	err := r.db.SelectContext(ctx, &list, query, campaignID)
	if err != nil {
		return nil, fmt.Errorf("list campaign tasks: %w", err)
	}
	return list, nil
}

func (r *Repository) ListCampaignLogs(ctx context.Context, campaignID string) ([]*CampaignLogItem, error) {
	// Per-device entries are written against the device, so they are found
	// through the campaign's own task rows -- by index -- rather than by a
	// LIKE over every audit detail, which scanned audit_logs on every 3-second
	// poll of an open campaign.
	var list []*CampaignLogItem
	query := `
		SELECT a.id, a.actor_type, a.actor_id, a.action, a.target_id, a.details, a.created_at
		FROM audit_logs a
		WHERE a.target_id = ?
		   OR (a.target_id IN (SELECT device_id FROM device_update_tasks WHERE campaign_id = ?)
		       AND a.action LIKE 'agent_update.%'
		       AND a.created_at >= (SELECT created_at FROM update_campaigns WHERE id = ?))
		ORDER BY a.created_at DESC
		LIMIT 100
	`
	err := r.db.SelectContext(ctx, &list, query, campaignID, campaignID, campaignID)
	if err != nil {
		return nil, fmt.Errorf("list campaign logs: %w", err)
	}
	return list, nil
}

// TransitionCampaignStatus moves a campaign to `to` only from one of the
// `from` statuses, and reports whether it did. Starting a campaign used to
// write in_progress whatever the campaign was, so a double click, or a POST
// for a running, finished or cancelled campaign, ran the whole rollout again
// and gave every device a second task.
func (r *Repository) TransitionCampaignStatus(ctx context.Context, id, to string, from ...string) (bool, error) {
	if len(from) == 0 {
		return false, fmt.Errorf("transition campaign: no source status")
	}
	args := []any{to, time.Now().UTC(), id}
	marks := make([]string, len(from))
	for i, f := range from {
		marks[i] = "?"
		args = append(args, f)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE update_campaigns SET status = ?, updated_at = ?
		WHERE id = ? AND status IN (`+strings.Join(marks, ", ")+`)`, args...)
	if err != nil {
		return false, fmt.Errorf("transition campaign: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CreateUpdateTask inserts a one-off task. It is released at once: there is
// no wave to wait for, so a pending row means "waiting for the device".
func (r *Repository) CreateUpdateTask(ctx context.Context, t *DeviceUpdateTask) error {
	if t.ID == "" {
		t.ID = newID()
	}
	now := time.Now().UTC()
	t.CreatedAt = now
	if t.Status == "" {
		t.Status = "pending"
	}
	if t.ReleasedAt == nil {
		t.ReleasedAt = &now
	}
	if err := insertTask(ctx, r.db, t); err != nil {
		return fmt.Errorf("create update task: %w", err)
	}
	return nil
}

// CreateCampaignTasks inserts every task of a campaign in one transaction, in
// the order given. Creating them all up front is what lets the campaign know
// how much work is left: tasks used to be created wave by wave, so a first
// wave that produced nothing made the rollup see zero tasks and close a
// campaign whose later waves had not run yet.
func (r *Repository) CreateCampaignTasks(ctx context.Context, tasks []*DeviceUpdateTask) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create campaign tasks: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	for i, t := range tasks {
		if t.ID == "" {
			t.ID = newID()
		}
		// Waves release in created_at order; a microsecond apart keeps the
		// operator's target order on both databases.
		t.CreatedAt = now.Add(time.Duration(i) * time.Microsecond)
		if t.Status == "" {
			t.Status = TaskStatusPending
		}
		if err := insertTask(ctx, tx, t); err != nil {
			return fmt.Errorf("create campaign task: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create campaign tasks: %w", err)
	}
	return nil
}

func insertTask(ctx context.Context, ex sqlx.ExecerContext, t *DeviceUpdateTask) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO device_update_tasks (
			id, campaign_id, device_id, from_version, target_version,
			status, error_message, dispatched_at, completed_at, created_at, released_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.CampaignID, t.DeviceID, t.FromVersion, t.TargetVersion,
		t.Status, t.ErrorMessage, t.DispatchedAt, t.CompletedAt, t.CreatedAt, t.ReleasedAt,
	)
	return err
}

// ReleaseNextWave lets the next batch of a campaign's waiting tasks go and
// schedules the wave after it. It returns the tasks this call released; a
// task is released once even if two callers race, because each release is
// conditional on the task still being unreleased.
func (r *Repository) ReleaseNextWave(ctx context.Context, campaignID string, batch int, stagger time.Duration) ([]*DeviceUpdateTask, error) {
	if batch <= 0 {
		batch = 25
	}
	now := time.Now().UTC()
	var ids []string
	if err := r.db.SelectContext(ctx, &ids, `
		SELECT id FROM device_update_tasks
		WHERE campaign_id = ? AND released_at IS NULL AND status = 'pending'
		ORDER BY created_at, id
		LIMIT ?`, campaignID, batch); err != nil {
		return nil, fmt.Errorf("next wave: %w", err)
	}
	var released []*DeviceUpdateTask
	for _, id := range ids {
		res, err := r.db.ExecContext(ctx, `UPDATE device_update_tasks SET released_at = ?
			WHERE id = ? AND released_at IS NULL AND status = 'pending'`, now, id)
		if err != nil {
			return released, fmt.Errorf("release task: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		t, err := r.GetUpdateTask(ctx, id)
		if err != nil {
			return released, err
		}
		released = append(released, t)
	}

	var waiting int
	if err := r.db.GetContext(ctx, &waiting, `
		SELECT COUNT(*) FROM device_update_tasks
		WHERE campaign_id = ? AND released_at IS NULL AND status = 'pending'`, campaignID); err != nil {
		return released, fmt.Errorf("count waiting tasks: %w", err)
	}
	var next *time.Time
	if waiting > 0 {
		t := now.Add(stagger)
		next = &t
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE update_campaigns SET next_wave_at = ?, updated_at = ?
		WHERE id = ? AND status = 'in_progress'`, next, now, campaignID); err != nil {
		return released, fmt.Errorf("schedule next wave: %w", err)
	}
	return released, nil
}

// DueWaveCampaigns returns the running campaigns whose next wave is due.
func (r *Repository) DueWaveCampaigns(ctx context.Context, now time.Time) ([]*UpdateCampaign, error) {
	var list []*UpdateCampaign
	err := r.db.SelectContext(ctx, &list, `
		SELECT * FROM update_campaigns
		WHERE status = 'in_progress' AND next_wave_at IS NOT NULL AND next_wave_at <= ?
		ORDER BY next_wave_at`, now)
	if err != nil {
		return nil, fmt.Errorf("due waves: %w", err)
	}
	return list, nil
}

// DevicesWithQueuedTasks lists devices that have a released task still
// waiting to be sent. The wave loop retries those that are online: a send can
// fail on a busy socket, and a device that stays connected never triggers the
// reconnect flush again.
func (r *Repository) DevicesWithQueuedTasks(ctx context.Context) ([]string, error) {
	var ids []string
	err := r.db.SelectContext(ctx, &ids, `
		SELECT DISTINCT device_id FROM device_update_tasks
		WHERE status = 'pending' AND released_at IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("devices with queued tasks: %w", err)
	}
	return ids, nil
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
		WHERE device_id = ? AND status = 'pending' AND released_at IS NOT NULL
		ORDER BY created_at ASC`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("pending tasks for %s: %w", deviceID, err)
	}
	return tasks, nil
}

// ActiveTasksForDevice returns tasks in non-terminal states for a device.
func (r *Repository) ActiveTasksForDevice(ctx context.Context, deviceID string) ([]*DeviceUpdateTask, error) {
	var tasks []*DeviceUpdateTask
	err := r.db.SelectContext(ctx, &tasks, `
		SELECT * FROM device_update_tasks
		WHERE device_id = ? AND status IN ('pending', 'dispatched', 'downloading', 'verifying', 'swapping')
		ORDER BY created_at ASC`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("active tasks for %s: %w", deviceID, err)
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
		WHERE id = ? AND status = 'dispatched'`, taskID)
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
		SET status = ?, error_message = ?, completed_at = COALESCE(?, completed_at), progress_at = ?
		WHERE id = ?
	`
	args := []any{status, errMsg, completedAt, now, taskID}
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

// SweepInterval is how often StartSweep reaps orphaned update tasks.
const SweepInterval = 30 * time.Second

// TaskTimeoutGrace is how long a task that was sent may go without a report
// while its device is offline before it is marked failed.
const TaskTimeoutGrace = 10 * time.Minute

// StaleProgressAfter is how long a sent task may go without any report, even
// on a device that looks online, before it is marked failed.
const StaleProgressAfter = 15 * time.Minute

// QueuedTaskExpiry is how long a released task waits for its device to come
// online. A laptop away for a week still gets its update; after that the task
// fails as never connected, so the campaign can finish.
const QueuedTaskExpiry = 7 * 24 * time.Hour

// WaveInterval is how often the wave loop releases due waves.
const WaveInterval = 10 * time.Second

// SyncCampaignStatusByID recomputes a rollout's status from its task rows and
// writes it once every task has reached a terminal state.
//
// Every task exists from the start, including those still waiting for their
// wave, so "every task terminal" really means the rollout is over. The result
// is completed when every device updated, failed when none did, and partial
// for a mix -- a campaign that updated 3 of 500 devices used to read
// "completed".
//
// A campaign with 0 tasks resolves to 'completed' instead of being stuck in
// 'in_progress'.
func (r *Repository) SyncCampaignStatusByID(ctx context.Context, campaignID string) error {
	if campaignID == "" {
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
		WHERE campaign_id = ?`, campaignID); err != nil {
		return err
	}

	if counts.Total != counts.Done {
		return nil
	}

	var status string
	switch {
	case counts.Total == 0 || counts.Succeeded == counts.Total:
		status = "completed"
	case counts.Succeeded == 0:
		status = "failed"
	default:
		status = "partial"
	}
	// Only a running campaign is finished here. A cancelled one fails its
	// tasks, and a late agent report or the sweeper calling this afterwards
	// used to see every task done and overwrite 'cancelled'.
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `UPDATE update_campaigns SET status = ?, updated_at = ?, next_wave_at = NULL
		WHERE id = ? AND status = 'in_progress'`, status, now, campaignID)
	return err
}

// SyncCampaignStatus recomputes a rollout's status from its task rows and
// writes it once every task has reached a terminal state.
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
	return r.SyncCampaignStatusByID(ctx, *campaignID)
}

// AbandonOrphanedTasks fails the update tasks that can no longer finish and
// rolls up their campaigns:
//
//  1. A sent task whose device went offline and has not reported for grace.
//  2. A sent task with no report for StaleProgressAfter, online or not -- the
//     agent crashed, or rebooted into the old binary.
//  3. A queued task whose device has not come online for QueuedTaskExpiry.
//
// A queued task is NOT failed just because its device is offline: that is
// what queued means. The sweep used to include 'pending' in rule 1, so an
// update queued for a sleeping laptop failed ten minutes later and the
// reconnect flush had nothing left to send. Rules 1 and 2 measure from the
// last report, not from the send, so a slow download that keeps reporting is
// not killed at the 15-minute mark.
func (r *Repository) AbandonOrphanedTasks(ctx context.Context, grace time.Duration) (int64, error) {
	now := time.Now().UTC()
	offlineCutoff := now.Add(-grace)
	staleCutoff := now.Add(-StaleProgressAfter)
	queuedCutoff := now.Add(-QueuedTaskExpiry)

	const lastHeard = `COALESCE(t.progress_at, t.dispatched_at, t.created_at)`
	type rule struct {
		where string
		args  []any
		msg   string
	}
	rules := []rule{
		{
			where: `t.status IN ('dispatched', 'downloading', 'verifying', 'swapping')
				AND ` + lastHeard + ` < ?
				AND t.device_id IN (SELECT id FROM devices WHERE status != 'online')`,
			args: []any{offlineCutoff},
			msg:  "Endpoint went offline before reporting the update result",
		},
		{
			where: `t.status IN ('dispatched', 'downloading', 'verifying', 'swapping')
				AND ` + lastHeard + ` < ?`,
			args: []any{staleCutoff},
			msg:  "Update task timed out after 15 minutes without a progress report",
		},
		{
			where: `t.status = 'pending' AND t.released_at IS NOT NULL AND t.released_at < ?`,
			args:  []any{queuedCutoff},
			msg:   "never_connected: the device did not come online within 7 days",
		},
	}

	var total int64
	seen := map[string]bool{}
	var campaignIDs []string
	for _, ru := range rules {
		var ids []string
		if err := r.db.SelectContext(ctx, &ids, `SELECT DISTINCT t.campaign_id FROM device_update_tasks t
			WHERE t.campaign_id IS NOT NULL AND `+ru.where, ru.args...); err != nil {
			return total, fmt.Errorf("find campaigns to roll up: %w", err)
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				campaignIDs = append(campaignIDs, id)
			}
		}
		args := append([]any{ru.msg, now}, ru.args...)
		res, err := r.db.ExecContext(ctx, `UPDATE device_update_tasks
			SET status = 'failed', error_message = ?, completed_at = ?
			WHERE id IN (SELECT t.id FROM device_update_tasks t WHERE `+ru.where+`)`, args...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}

	for _, cid := range campaignIDs {
		if err := r.SyncCampaignStatusByID(ctx, cid); err != nil {
			log.Warn().Err(err).Str("campaign_id", cid).Msg("roll up campaign after sweep")
		}
	}
	return total, nil
}

// StartSweep reaps orphaned update tasks until ctx is cancelled.
func (r *Repository) StartSweep(ctx context.Context, interval, grace time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := r.AbandonOrphanedTasks(ctx, grace); err != nil {
					log.Warn().Err(err).Msg("agent update sweep error")
				}
			}
		}
	}()
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

// GetDeviceArch reads the architecture recorded in device_inventory, or falls back to "amd64".
func (r *Repository) GetDeviceArch(ctx context.Context, deviceID string) string {
	var osDetailStr string
	err := r.db.GetContext(ctx, &osDetailStr, `SELECT os_detail FROM device_inventory WHERE device_id = ?`, deviceID)
	if err != nil || osDetailStr == "" {
		return "amd64"
	}
	var detail struct {
		Architecture string `json:"architecture"`
	}
	if err := json.Unmarshal([]byte(osDetailStr), &detail); err != nil || detail.Architecture == "" {
		return "amd64"
	}
	return normalizeArch(detail.Architecture)
}

func normalizeArch(raw string) string {
	r := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.Contains(r, "arm64") || strings.Contains(r, "aarch64"):
		return "arm64"
	case strings.Contains(r, "amd64") || strings.Contains(r, "x86_64") || strings.Contains(r, "64-bit") || strings.Contains(r, "x64"):
		return "amd64"
	case strings.Contains(r, "386") || strings.Contains(r, "32-bit") || strings.Contains(r, "x86"):
		return "386"
	case strings.Contains(r, "arm"):
		return "arm"
	default:
		if r != "" {
			return r
		}
		return "amd64"
	}
}

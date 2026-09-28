package patchmgmt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

var (
	ErrNotFound = errors.New("record not found")
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) UpsertPatches(ctx context.Context, deviceID string, patches []DevicePatch) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	// Track new patch IDs in this scan
	activePatchIDs := make(map[string]bool)

	for _, p := range patches {
		if p.ID == "" {
			p.ID = NewID()
		}
		if p.DeviceID == "" {
			p.DeviceID = deviceID
		}
		if p.DiscoveredAt.IsZero() {
			p.DiscoveredAt = now
		}
		p.UpdatedAt = now
		activePatchIDs[p.PatchID] = true

		rebootReqInt := 0
		if p.RebootRequired {
			rebootReqInt = 1
		}

		query := `
			INSERT INTO device_patches (
				id, device_id, patch_id, title, description, severity, category,
				kb_id, size_bytes, installed_state, reboot_required, discovered_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(device_id, patch_id) DO UPDATE SET
				title = excluded.title,
				description = excluded.description,
				severity = excluded.severity,
				category = excluded.category,
				kb_id = excluded.kb_id,
				size_bytes = excluded.size_bytes,
				installed_state = excluded.installed_state,
				reboot_required = excluded.reboot_required,
				updated_at = excluded.updated_at
		`
		if _, err := tx.ExecContext(ctx, query,
			p.ID, p.DeviceID, p.PatchID, p.Title, p.Description, p.Severity, p.Category,
			p.KBID, p.SizeBytes, p.InstalledState, rebootReqInt, p.DiscoveredAt, p.UpdatedAt,
		); err != nil {
			return fmt.Errorf("upsert patch %s: %w", p.PatchID, err)
		}
	}

	return tx.Commit()
}

func (r *Repository) ListDevicePatches(ctx context.Context, deviceID string, state string) ([]DevicePatch, error) {
	query := `
		SELECT
			p.id, p.device_id, p.patch_id, p.title, p.description, p.severity, p.category,
			p.kb_id, p.size_bytes, p.installed_state, p.reboot_required, p.discovered_at, p.updated_at,
			d.hostname, d.os_name
		FROM device_patches p
		JOIN devices d ON d.id = p.device_id
		WHERE p.device_id = ?
	`
	args := []any{deviceID}
	if state != "" {
		query += " AND p.installed_state = ?"
		args = append(args, state)
	}
	query += " ORDER BY p.severity = 'critical' DESC, p.severity = 'important' DESC, p.title ASC"

	rows := []DevicePatch{}
	if err := r.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("list device patches: %w", err)
	}
	return rows, nil
}

func (r *Repository) ListFleetPatches(ctx context.Context, limit int, state string) ([]DevicePatch, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
		SELECT
			p.id, p.device_id, p.patch_id, p.title, p.description, p.severity, p.category,
			p.kb_id, p.size_bytes, p.installed_state, p.reboot_required, p.discovered_at, p.updated_at,
			d.hostname, d.os_name
		FROM device_patches p
		JOIN devices d ON d.id = p.device_id
	`
	var args []any
	if state != "" {
		query += " WHERE p.installed_state = ?"
		args = append(args, state)
	}
	query += " ORDER BY p.severity = 'critical' DESC, p.severity = 'important' DESC, p.updated_at DESC LIMIT ?"
	args = append(args, limit)

	rows := []DevicePatch{}
	if err := r.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("list fleet patches: %w", err)
	}
	return rows, nil
}

func (r *Repository) GetFleetSummary(ctx context.Context) (FleetPatchSummary, error) {
	var summary FleetPatchSummary

	q1 := `SELECT COUNT(*) FROM device_patches WHERE installed_state = 'missing'`
	if err := r.db.GetContext(ctx, &summary.TotalMissingPatches, q1); err != nil {
		return summary, err
	}

	q2 := `SELECT COUNT(*) FROM device_patches WHERE installed_state = 'missing' AND (severity = 'critical' OR category = 'security')`
	if err := r.db.GetContext(ctx, &summary.CriticalSecurityPatches, q2); err != nil {
		return summary, err
	}

	q3 := `SELECT COUNT(DISTINCT device_id) FROM device_patches WHERE reboot_required = 1`
	if err := r.db.GetContext(ctx, &summary.RebootPendingDevices, q3); err != nil {
		return summary, err
	}

	q4 := `SELECT COUNT(DISTINCT device_id) FROM device_patches WHERE installed_state = 'missing'`
	if err := r.db.GetContext(ctx, &summary.VulnerableDevices, q4); err != nil {
		return summary, err
	}

	return summary, nil
}

func (r *Repository) CreateInstallJob(ctx context.Context, job *PatchInstallJob) error {
	query := `
		INSERT INTO patch_install_jobs (
			id, device_id, operator_id, patch_ids, status, reboot_policy, reboot_required,
			output_log, error_message, started_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	rebootReq := 0
	if job.RebootRequired {
		rebootReq = 1
	}
	_, err := r.db.ExecContext(ctx, query,
		job.ID, job.DeviceID, job.OperatorID, job.PatchIDs, job.Status, job.RebootPolicy,
		rebootReq, job.OutputLog, job.ErrorMessage, job.StartedAt,
	)
	if err != nil {
		return fmt.Errorf("create patch install job: %w", err)
	}
	return nil
}

func (r *Repository) GetInstallJob(ctx context.Context, jobID string) (*PatchInstallJob, error) {
	query := `
		SELECT
			j.id, j.device_id, j.operator_id, j.patch_ids, j.status, j.reboot_policy,
			j.reboot_required, j.output_log, j.error_message, j.started_at, j.completed_at,
			u.username as operator_name, d.hostname
		FROM patch_install_jobs j
		JOIN users u ON u.id = j.operator_id
		JOIN devices d ON d.id = j.device_id
		WHERE j.id = ?
	`
	var job PatchInstallJob
	if err := r.db.GetContext(ctx, &job, query, jobID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get patch install job: %w", err)
	}
	return &job, nil
}

func (r *Repository) ListInstallJobs(ctx context.Context, deviceID string, limit int) ([]PatchInstallJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query := `
		SELECT
			j.id, j.device_id, j.operator_id, j.patch_ids, j.status, j.reboot_policy,
			j.reboot_required, j.output_log, j.error_message, j.started_at, j.completed_at,
			u.username as operator_name, d.hostname
		FROM patch_install_jobs j
		JOIN users u ON u.id = j.operator_id
		JOIN devices d ON d.id = j.device_id
		WHERE j.device_id = ?
		ORDER BY j.started_at DESC
		LIMIT ?
	`
	rows := []PatchInstallJob{}
	if err := r.db.SelectContext(ctx, &rows, query, deviceID, limit); err != nil {
		return nil, fmt.Errorf("list patch install jobs: %w", err)
	}
	return rows, nil
}

func (r *Repository) UpdateInstallJobResult(ctx context.Context, rep PatchInstallReport) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	rebootReq := 0
	if rep.RebootRequired {
		rebootReq = 1
	}

	query := `
		UPDATE patch_install_jobs
		SET status = ?, reboot_required = ?, output_log = ?, error_message = ?, completed_at = ?
		WHERE id = ?
	`
	if _, err := tx.ExecContext(ctx, query,
		rep.Status, rebootReq, rep.OutputLog, rep.ErrorMessage, now, rep.JobID,
	); err != nil {
		return fmt.Errorf("update patch install job: %w", err)
	}

	// If successful, update the installed_state of the patches in device_patches
	if rep.Status == JobStatusCompleted {
		var job PatchInstallJob
		if err := tx.GetContext(ctx, &job, `SELECT device_id, patch_ids FROM patch_install_jobs WHERE id = ?`, rep.JobID); err == nil {
			var patchIDs []string
			if json.Unmarshal([]byte(job.PatchIDs), &patchIDs) == nil {
				for _, pid := range patchIDs {
					_, _ = tx.ExecContext(ctx, `
						UPDATE device_patches
						SET installed_state = 'installed', reboot_required = ?, updated_at = ?
						WHERE device_id = ? AND patch_id = ?
					`, rebootReq, now, job.DeviceID, pid)
				}
			}
		}
	}

	return tx.Commit()
}

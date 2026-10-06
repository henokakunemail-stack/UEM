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

	if err := reconcileAbsentPatches(ctx, tx, deviceID, patches); err != nil {
		return err
	}

	return tx.Commit()
}

// reconcileAbsentPatches marks rows for this device that the scan no longer
// reports as installed. Without it a superseded update stays 'missing' forever:
// Windows Update drops an offer once a newer cumulative supersedes it, and the
// row that described the old one was never removed anywhere, so the console
// kept counting it and the install button kept offering a KB the machine cannot
// fetch.
//
// 'installed' is the right state rather than a deletion. The row is the history
// that this update was once offered, and an operator auditing last quarter needs
// it; it just stops being pending.
//
// An empty scan is a real answer -- this device is up to date -- so it does
// reconcile, and every row is the correct one to settle. It is safe only because
// the scanners refuse to report an empty list for a host they could not scan:
// a machine with no package manager, or a Windows Update service that did not
// answer, is now a failed scan rather than a compliant one.
func reconcileAbsentPatches(ctx context.Context, tx *sqlx.Tx, deviceID string, patches []DevicePatch) error {
	// Every patch_id the scan still reports, deduplicated. Those are the ones
	// that stay pending; the upsert above already wrote their state.
	stillReported := make([]string, 0, len(patches))
	seen := make(map[string]bool, len(patches))
	for _, p := range patches {
		if seen[p.PatchID] {
			continue
		}
		seen[p.PatchID] = true
		stillReported = append(stillReported, p.PatchID)
	}
	if len(stillReported) == 0 {
		// Nothing to protect, and sqlx.In renders an empty slice as IN () -- a
		// syntax error SQLite rejects. Settle-all would need a second query with
		// no NOT IN clause, and that is the one statement no scan should be able
		// to trigger: a device that reports an empty list would clear every row
		// it owns and read as fully compliant. The scanners now fail rather than
		// report a fake empty list, but an agent is still the one saying so.
		//
		// The cost is bounded. A ghost row needs a scan that still reports
		// something to be swept, so a fully patched device keeps a superseded KB
		// on its list until the next update lands. Wrong for a few days, and never
		// able to install, rather than a device that can be declared compliant
		// because it went quiet.
		return nil
	}

	query, args, err := sqlx.In(`
		UPDATE device_patches
		SET installed_state = ?, updated_at = ?
		WHERE device_id = ? AND patch_id NOT IN (?)
	`, StateInstalled, time.Now().UTC(), deviceID, stillReported)
	if err != nil {
		return fmt.Errorf("build reconcile query: %w", err)
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("reconcile absent patches for device %s: %w", deviceID, err)
	}
	return nil
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

// UpdateInstallJobResult records an install outcome against the job named in the
// report, and only for the device the report came from.
//
// The ownership check belongs in the WHERE clause rather than as a separate read:
// a read-then-write leaves a window for another request to take the job in
// between, and the two can be done in one statement here. Without the device_id
// term this was a cross-device write -- any agent could complete, fail, or
// overwrite the output log of any other agent's install job, and the audit
// record would name the attacking device as the one that did it.
func (r *Repository) UpdateInstallJobResult(ctx context.Context, deviceID string, rep PatchInstallReport) error {
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
		WHERE id = ? AND device_id = ?
	`
	res, err := tx.ExecContext(ctx, query,
		rep.Status, rebootReq, rep.OutputLog, rep.ErrorMessage, now, rep.JobID, deviceID,
	)
	if err != nil {
		return fmt.Errorf("update patch install job: %w", err)
	}
	// Zero rows means the job does not exist or belongs to another device. Both
	// are refused identically, so a caller cannot use this endpoint to probe
	// which job ids exist.
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}

	// If successful, update the installed_state of the patches in device_patches
	if rep.Status == JobStatusCompleted {
		var job PatchInstallJob
		if err := tx.GetContext(ctx, &job,
			`SELECT device_id, patch_ids FROM patch_install_jobs WHERE id = ? AND device_id = ?`,
			rep.JobID, deviceID); err == nil {
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

package patchmgmt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"
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

	// Only installed patches can be waiting on a reboot; a missing one that
	// a scan tagged as reboot-requiring has not been installed yet.
	q3 := `SELECT COUNT(DISTINCT device_id) FROM device_patches WHERE reboot_required = 1 AND installed_state = 'installed'`
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
			output_log, error_message, started_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	rebootReq := 0
	if job.RebootRequired {
		rebootReq = 1
	}
	_, err := r.db.ExecContext(ctx, query,
		job.ID, job.DeviceID, job.OperatorID, job.PatchIDs, job.Status, job.RebootPolicy,
		rebootReq, job.OutputLog, job.ErrorMessage, job.StartedAt, job.StartedAt,
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
	terminal := rep.Status == JobStatusCompleted || rep.Status == JobStatusFailed
	if !terminal && rep.Status != JobStatusInstalling {
		return ErrInvalidStatus
	}

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
	var completedAt *time.Time
	if terminal {
		completedAt = &now
	}

	// A finished job is not rewritten: a retried or late report used to
	// overwrite the result and restamp completed_at every time. The one
	// exception is a job the sweep gave up on (no_report): if the agent's
	// result does arrive, it is the truth and replaces the guess.
	query := `
		UPDATE patch_install_jobs
		SET status = ?, reboot_required = ?, output_log = ?, error_message = ?,
			completed_at = COALESCE(?, completed_at), updated_at = ?
		WHERE id = ? AND device_id = ?
		  AND (status NOT IN ('completed', 'failed')
		       OR (? AND status = 'failed' AND error_message LIKE 'no_report:%'))
	`
	res, err := tx.ExecContext(ctx, query,
		rep.Status, rebootReq, rep.OutputLog, rep.ErrorMessage, completedAt, now, rep.JobID, deviceID, terminal,
	)
	if err != nil {
		return fmt.Errorf("update patch install job: %w", err)
	}
	// Zero rows means the job does not exist, belongs to another device, or
	// already finished. The first two are refused identically, so a caller
	// cannot use this endpoint to probe which job ids exist.
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		var owned int
		if err := tx.GetContext(ctx, &owned, `SELECT COUNT(*) FROM patch_install_jobs WHERE id = ? AND device_id = ?`,
			rep.JobID, deviceID); err == nil && owned == 1 {
			return ErrJobFinished
		}
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

// ErrInvalidStatus is an install report with a status the job cannot take.
var ErrInvalidStatus = errors.New("status must be installing, completed or failed")

// ErrJobFinished is a report for a job that already finished.
var ErrJobFinished = errors.New("install job already finished")

// FailJob closes a job that never ran -- the send failed, or the agent
// refused the command -- with the reason. It only touches a job that has not
// finished, and reports whether it changed one.
func (r *Repository) FailJob(ctx context.Context, jobID, deviceID, reason string) (bool, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE patch_install_jobs SET status = 'failed', error_message = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND device_id = ? AND status IN ('pending', 'dispatched', 'installing')`,
		reason, now, now, jobID, deviceID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// JobOwned reports whether jobID is an install job of deviceID.
func (r *Repository) JobOwned(ctx context.Context, jobID, deviceID string) (bool, error) {
	var n int
	err := r.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM patch_install_jobs WHERE id = ? AND device_id = ?`, jobID, deviceID)
	return n == 1, err
}

// ClearRebootRequired drops the reboot flag on a device's patches after the
// device reported that no reboot is pending. The flag was only ever set, so
// the console's "reboot pending" count only ever grew.
func (r *Repository) ClearRebootRequired(ctx context.Context, deviceID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE device_patches SET reboot_required = 0, updated_at = ?
		WHERE device_id = ? AND reboot_required = 1`, time.Now().UTC(), deviceID)
	return err
}

// JobNoReportAfter is how long a sent install job may go without a result
// before it is failed. Windows Update installs can be slow, but an agent that
// is alive reports within this.
const JobNoReportAfter = 30 * time.Minute

// SweepInterval is how often the job sweep runs.
const SweepInterval = time.Minute

// AbandonSilentJobs fails install jobs with no result for JobNoReportAfter.
// Nothing closed them before: a job whose agent died or whose result was lost
// said 'dispatched' forever.
func (r *Repository) AbandonSilentJobs(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE patch_install_jobs
		SET status = 'failed', completed_at = ?, updated_at = ?,
			error_message = 'no_report: the agent sent no result within 30 minutes; check the device and scan again'
		WHERE status IN ('pending', 'dispatched', 'installing')
		  AND COALESCE(updated_at, started_at) < ?`, now, now, now.Add(-JobNoReportAfter))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// StartSweep runs AbandonSilentJobs until ctx is cancelled.
func (r *Repository) StartSweep(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n, err := r.AbandonSilentJobs(ctx); err != nil {
					log.Warn().Err(err).Msg("patch job sweep")
				} else if n > 0 {
					log.Warn().Int64("jobs", n).Msg("patch job sweep: failed install jobs with no result")
				}
			}
		}
	}()
}

package softwaredeployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

var (
	ErrNotFound = errors.New("not found")
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// CreatePackage inserts a new package into the software_packages table.
func (r *Repository) CreatePackage(ctx context.Context, p SoftwarePackage) error {
	query := `
		INSERT INTO software_packages (
			id, name, version, os_target, package_type, file_name,
			file_size, sha256, storage_path, install_args, uninstall_args,
			created_at, updated_at
		) VALUES (
			:id, :name, :version, :os_target, :package_type, :file_name,
			:file_size, :sha256, :storage_path, :install_args, :uninstall_args,
			:created_at, :updated_at
		)`
	_, err := r.db.NamedExecContext(ctx, query, p)
	return err
}

// GetPackage retrieves a single package by its ID.
func (r *Repository) GetPackage(ctx context.Context, id string) (SoftwarePackage, error) {
	var p SoftwarePackage
	query := `SELECT * FROM software_packages WHERE id = ?`
	err := r.db.GetContext(ctx, &p, query, id)
	if errors.Is(err, sql.ErrNoRows) {
		return SoftwarePackage{}, ErrNotFound
	}
	return p, err
}

// ListPackages returns all software packages sorted by creation date descending.
func (r *Repository) ListPackages(ctx context.Context) ([]SoftwarePackage, error) {
	pkgs := []SoftwarePackage{}
	query := `SELECT * FROM software_packages ORDER BY created_at DESC`
	err := r.db.SelectContext(ctx, &pkgs, query)
	if err != nil {
		return nil, err
	}
	return pkgs, nil
}

// DeletePackage removes a package by its ID.
func (r *Repository) DeletePackage(ctx context.Context, id string) error {
	query := `DELETE FROM software_packages WHERE id = ?`
	res, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResolveTargetDevices returns the list of active device IDs for a target specification.
func (r *Repository) ResolveTargetDevices(ctx context.Context, targetType, targetID, osTarget string) ([]string, error) {
	var deviceIDs []string

	switch targetType {
	case TargetDevice:
		var d DeviceCheck
		query := `SELECT id, os_name, retired_at FROM devices WHERE id = ?`
		err := r.db.GetContext(ctx, &d, query, targetID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("device %s: %w", targetID, ErrNotFound)
		}
		if err != nil {
			return nil, err
		}
		if d.RetiredAt != nil {
			return nil, fmt.Errorf("device %s is retired", targetID)
		}
		deviceIDs = append(deviceIDs, d.ID)

	case TargetGroup:
		query := `
			SELECT d.id
			FROM devices d
			JOIN device_group_members m ON d.id = m.device_id
			WHERE m.group_id = ? AND d.retired_at IS NULL`
		args := []any{targetID}
		if osTarget != "" {
			// Parameterised, never concatenated. os_target is a raw multipart
			// form field (handler.go:94) that was persisted in software_packages
			// and only reached this function later, at deploy time — so the
			// injection was stored, not reflected.
			query += " AND d.os_name = ?"
			args = append(args, osTarget)
		}
		err := r.db.SelectContext(ctx, &deviceIDs, query, args...)
		if err != nil {
			return nil, err
		}

	case TargetAll:
		query := `SELECT id FROM devices WHERE retired_at IS NULL`
		var args []any
		if osTarget != "" {
			query += " AND os_name = ?"
			args = append(args, osTarget)
		}
		err := r.db.SelectContext(ctx, &deviceIDs, query, args...)
		if err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("unsupported target_type %q", targetType)
	}

	return deviceIDs, nil
}

type DeviceCheck struct {
	ID        string     `db:"id"`
	OSName    string     `db:"os_name"`
	RetiredAt *time.Time `db:"retired_at"`
}

// CreateDeploymentTx atomically creates a deployment record and all associated endpoint tasks.
func (r *Repository) CreateDeploymentTx(ctx context.Context, dep SoftwareDeployment, deviceIDs []string) ([]DeploymentTask, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Insert Deployment
	queryDep := `
		INSERT INTO software_deployments (
			id, package_id, name, target_type, target_id, created_by, status, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := tx.ExecContext(ctx, queryDep,
		dep.ID, dep.PackageID, dep.Name, dep.TargetType, dep.TargetID, dep.CreatedBy, dep.Status, dep.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("insert deployment: %w", err)
	}

	// Insert Tasks
	tasks := make([]DeploymentTask, 0, len(deviceIDs))
	now := time.Now().UTC()
	queryTask := `
		INSERT INTO deployment_tasks (
			id, deployment_id, package_id, device_id, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`

	for _, devID := range deviceIDs {
		t := DeploymentTask{
			ID:           NewID(),
			DeploymentID: dep.ID,
			PackageID:    dep.PackageID,
			DeviceID:     devID,
			Status:       TaskStatusPending,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if _, err := tx.ExecContext(ctx, queryTask,
			t.ID, t.DeploymentID, t.PackageID, t.DeviceID, t.Status, t.CreatedAt, t.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("insert task for device %s: %w", devID, err)
		}
		tasks = append(tasks, t)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return tasks, nil
}

// ListDeployments returns deployments with aggregated status metrics.
func (r *Repository) ListDeployments(ctx context.Context) ([]SoftwareDeployment, error) {
	query := `
		SELECT
			d.id, d.package_id, d.name, d.target_type, d.target_id, d.created_by, d.status,
			d.created_at, d.completed_at,
			p.name AS package_name,
			p.version AS package_version,
			COUNT(t.id) AS total_tasks,
			COALESCE(SUM(CASE WHEN t.status = 'success' THEN 1 ELSE 0 END), 0) AS success_tasks,
			COALESCE(SUM(CASE WHEN t.status IN ('failed', 'failed_lost') THEN 1 ELSE 0 END), 0) AS failed_tasks
		FROM software_deployments d
		LEFT JOIN software_packages p ON d.package_id = p.id
		LEFT JOIN deployment_tasks t ON d.id = t.deployment_id
		GROUP BY d.id
		ORDER BY d.created_at DESC`

	deps := []SoftwareDeployment{}
	err := r.db.SelectContext(ctx, &deps, query)
	if err != nil {
		return nil, err
	}
	return deps, nil
}

// GetDeployment returns a deployment with enriched details.
func (r *Repository) GetDeployment(ctx context.Context, id string) (SoftwareDeployment, error) {
	query := `
		SELECT
			d.id, d.package_id, d.name, d.target_type, d.target_id, d.created_by, d.status,
			d.created_at, d.completed_at,
			p.name AS package_name,
			p.version AS package_version,
			COUNT(t.id) AS total_tasks,
			COALESCE(SUM(CASE WHEN t.status = 'success' THEN 1 ELSE 0 END), 0) AS success_tasks,
			COALESCE(SUM(CASE WHEN t.status IN ('failed', 'failed_lost') THEN 1 ELSE 0 END), 0) AS failed_tasks
		FROM software_deployments d
		LEFT JOIN software_packages p ON d.package_id = p.id
		LEFT JOIN deployment_tasks t ON d.id = t.deployment_id
		WHERE d.id = ?
		GROUP BY d.id`

	var dep SoftwareDeployment
	err := r.db.GetContext(ctx, &dep, query, id)
	if errors.Is(err, sql.ErrNoRows) {
		return SoftwareDeployment{}, ErrNotFound
	}
	return dep, err
}

// ListDeploymentTasks returns all tasks under a deployment.
func (r *Repository) ListDeploymentTasks(ctx context.Context, deploymentID string) ([]DeploymentTask, error) {
	query := `
		SELECT
			t.id, t.deployment_id, t.package_id, t.device_id, t.status,
			t.exit_code, t.output_log, t.error_message, t.created_at, t.updated_at, t.completed_at,
			dev.hostname, COALESCE(dev.site, '') AS site
		FROM deployment_tasks t
		JOIN devices dev ON t.device_id = dev.id
		WHERE t.deployment_id = ?
		ORDER BY dev.hostname ASC`

	tasks := []DeploymentTask{}
	err := r.db.SelectContext(ctx, &tasks, query, deploymentID)
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// UpdateTaskProgress updates an ongoing or completed task.
func (r *Repository) UpdateTaskProgress(ctx context.Context, report TaskProgressReport) error {
	now := time.Now().UTC()
	var completedAt *time.Time
	if report.Status == TaskStatusSuccess || report.Status == TaskStatusFailed {
		completedAt = &now
	}

	query := `
		UPDATE deployment_tasks
		SET status = ?, exit_code = ?, output_log = ?, error_message = ?, updated_at = ?, completed_at = COALESCE(?, completed_at)
		WHERE id = ?`

	res, err := r.db.ExecContext(ctx, query,
		report.Status, report.ExitCode, report.OutputLog, report.ErrorMessage, now, completedAt, report.TaskID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}

	// Update parent deployment status if all tasks are finished
	_ = r.syncDeploymentStatus(ctx, report.TaskID)
	return nil
}

// syncDeploymentStatus checks if all tasks for a deployment are done and marks it completed.
func (r *Repository) syncDeploymentStatus(ctx context.Context, taskID string) error {
	query := `
		SELECT deployment_id FROM deployment_tasks WHERE id = ?`
	var depID string
	if err := r.db.GetContext(ctx, &depID, query, taskID); err != nil {
		return err
	}
	return r.syncDeploymentStatusByID(ctx, depID)
}

// syncDeploymentStatusByID is the rollup for one deployment.
//
// 'done' counts success, failed and failed_lost together because all three are
// terminal, but the rollup has to know whether anything actually succeeded:
// every task failing used to land here with Done == Total and mark the parent
// 'completed', so a 0% deployment rendered a green Completed badge. The same
// now applies to a deployment whose every agent dropped offline.
func (r *Repository) syncDeploymentStatusByID(ctx context.Context, depID string) error {
	checkQuery := `
		SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN status IN ('success', 'failed', 'failed_lost') THEN 1 ELSE 0 END), 0) AS done,
			COALESCE(SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END), 0) AS succeeded
		FROM deployment_tasks
		WHERE deployment_id = ?`

	var counts struct {
		Total     int `db:"total"`
		Done      int `db:"done"`
		Succeeded int `db:"succeeded"`
	}
	if err := r.db.GetContext(ctx, &counts, checkQuery, depID); err != nil {
		return err
	}

	if counts.Total > 0 && counts.Total == counts.Done {
		now := time.Now().UTC()
		status := "completed"
		if counts.Succeeded == 0 {
			status = "failed"
		}
		updateDep := `UPDATE software_deployments SET status = ?, completed_at = ? WHERE id = ?`
		_, _ = r.db.ExecContext(ctx, updateDep, status, now, depID)
	}
	return nil
}

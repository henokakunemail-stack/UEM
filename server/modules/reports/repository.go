package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

type DeviceInventoryRow struct {
	ID           string    `json:"id" db:"id"`
	Hostname     string    `json:"hostname" db:"hostname"`
	OSName       string    `json:"os_name" db:"os_name"`
	OSVersion    string    `json:"os_version" db:"os_version"`
	AgentVersion string    `json:"agent_version" db:"agent_version"`
	Site         string    `json:"site" db:"site"`
	Status       string    `json:"status" db:"status"`
	RAMBytes     *int64    `json:"ram_bytes" db:"hw_ram_bytes"`
	DiskFreePct  *float64  `json:"disk_free_pct" db:"hw_disk_free_pct"`
	CPUModel     *string   `json:"cpu_model" db:"hw_cpu_model"`
	// A device that has been issued an enrollment token but has never checked
	// in has last_seen_at = NULL, and that is not the same fact as "seen at
	// 0001-01-01". So this stays a pointer and the exporter nils it out, the
	// same way Device.LastSeenAt and offlineRow.LastSeenAt already do.
	LastSeenAt *time.Time `json:"last_seen_at" db:"last_seen_at"`
	EnrolledAt time.Time  `json:"enrolled_at" db:"enrolled_at"`
}

type PatchComplianceRow struct {
	DeviceID       string    `json:"device_id" db:"device_id"`
	Hostname       string    `json:"hostname" db:"hostname"`
	OSName         string    `json:"os_name" db:"os_name"`
	Site           string    `json:"site" db:"site"`
	PatchID        string    `json:"patch_id" db:"patch_id"`
	Title          string    `json:"title" db:"title"`
	Severity       string    `json:"severity" db:"severity"`
	Category       string    `json:"category" db:"category"`
	InstalledState string    `json:"installed_state" db:"installed_state"`
	RebootRequired bool      `json:"reboot_required" db:"reboot_required"`
	DiscoveredAt   time.Time `json:"discovered_at" db:"discovered_at"`
}

type SoftwareInventoryRow struct {
	SoftwareName string `json:"software_name" db:"software"`
	DeviceCount  int    `json:"device_count" db:"device_count"`
}

type DeploymentReportRow struct {
	DeploymentID string     `json:"deployment_id" db:"deployment_id"`
	Name         string     `json:"deployment_name" db:"deployment_name"`
	PackageName  string     `json:"package_name" db:"package_name"`
	TargetType   string     `json:"target_type" db:"target_type"`
	DeviceID     string     `json:"device_id" db:"device_id"`
	Hostname     string     `json:"hostname" db:"hostname"`
	TaskStatus   string     `json:"task_status" db:"task_status"`
	ExitCode     *int       `json:"exit_code" db:"exit_code"`
	StartedAt    *time.Time `json:"started_at" db:"started_at"`
	CompletedAt  *time.Time `json:"completed_at" db:"completed_at"`
}

type AuditReportRow struct {
	ID        string    `json:"id" db:"id"`
	ActorType string    `json:"actor_type" db:"actor_type"`
	ActorID   string    `json:"actor_id" db:"actor_id"`
	Action    string    `json:"action" db:"action"`
	TargetID  *string   `json:"target_id" db:"target_id"`
	Details   *string   `json:"details" db:"details"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

func (r *Repository) GetDeviceInventoryReport(ctx context.Context, site, osName string) ([]DeviceInventoryRow, error) {
	query := `
		SELECT
			d.id, d.hostname, d.os_name,
			COALESCE(d.os_version, '') AS os_version,
			COALESCE(d.agent_version, '') AS agent_version,
			COALESCE(d.site, '') AS site, d.status,
			i.hw_ram_bytes, i.hw_disk_free_pct, i.hw_cpu_model, d.last_seen_at, d.enrolled_at
		FROM devices d
		LEFT JOIN device_inventory i ON i.device_id = d.id
		WHERE d.retired_at IS NULL
	`
	var args []any
	if site != "" {
		query += " AND d.site = ?"
		args = append(args, site)
	}
	if osName != "" {
		query += " AND d.os_name = ?"
		args = append(args, osName)
	}
	query += " ORDER BY d.hostname ASC"

	var rows []DeviceInventoryRow
	if err := r.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("device inventory report: %w", err)
	}
	return rows, nil
}

func (r *Repository) GetPatchComplianceReport(ctx context.Context, severity, state string) ([]PatchComplianceRow, error) {
	query := `
		SELECT
			p.device_id, d.hostname, d.os_name, COALESCE(d.site, '') AS site, p.patch_id, p.title,
			p.severity, p.category, p.installed_state, p.reboot_required, p.discovered_at
		FROM device_patches p
		JOIN devices d ON d.id = p.device_id
		WHERE d.retired_at IS NULL
	`
	var args []any
	if severity != "" {
		query += " AND p.severity = ?"
		args = append(args, severity)
	}
	if state != "" {
		query += " AND p.installed_state = ?"
		args = append(args, state)
	}
	query += " ORDER BY p.severity = 'critical' DESC, d.hostname ASC"

	var rows []PatchComplianceRow
	if err := r.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("patch compliance report: %w", err)
	}
	return rows, nil
}

func (r *Repository) GetDeploymentHistoryReport(ctx context.Context) ([]DeploymentReportRow, error) {
	query := `
		SELECT
			d.id as deployment_id, d.name as deployment_name, p.name as package_name,
			d.target_type, t.device_id, dev.hostname, t.status as task_status,
			t.exit_code, t.created_at as started_at, t.completed_at
		FROM deployment_tasks t
		JOIN software_deployments d ON d.id = t.deployment_id
		JOIN software_packages p ON p.id = t.package_id
		JOIN devices dev ON dev.id = t.device_id
		ORDER BY d.created_at DESC, dev.hostname ASC
		LIMIT 1000
	`
	var rows []DeploymentReportRow
	if err := r.db.SelectContext(ctx, &rows, query); err != nil {
		return nil, fmt.Errorf("deployment history report: %w", err)
	}
	return rows, nil
}

func (r *Repository) GetAuditTrailReport(ctx context.Context, action string, limit int) ([]AuditReportRow, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	// actor_id is also nullable, so it is COALESCEd into a plain string to match
	// AuditReportRow.ActorID. target_id and details stay nullable on purpose:
	// their fields are *string, and the export format distinguishes "no target"
	// (null) from "empty target" ("") — do not collapse the two.
	query := `
		SELECT id, actor_type, COALESCE(actor_id, '') AS actor_id, action, target_id, details, created_at
		FROM audit_logs
	`
	var args []any
	if action != "" {
		query += " WHERE action = ?"
		args = append(args, action)
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	var rows []AuditReportRow
	if err := r.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("audit trail report: %w", err)
	}
	return rows, nil
}

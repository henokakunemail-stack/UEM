package softwaredeployment

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewID produces a random 32-character hex ID.
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

const (
	OSTargetWindows = "windows"
	OSTargetLinux   = "linux"
	OSTargetMacOS   = "macos"

	PkgTypeMSI    = "msi"
	PkgTypeEXE    = "exe"
	PkgTypeDEB    = "deb"
	PkgTypeRPM    = "rpm"
	PkgTypePKG    = "pkg"
	PkgTypeScript = "script"

	TargetDevice = "device"
	TargetGroup  = "group"
	TargetAll    = "all"

	// Action names which of the two operations a deployment performs. It is a
	// column on the deployment rather than a separate table so that the list
	// view, the task drill-down, the rollup and the audit trail all read the
	// uninstall the same way they read an install.
	ActionInstall   = "install"
	ActionUninstall = "uninstall"

	TaskStatusPending     = "pending"
	TaskStatusDispatched  = "dispatched"
	TaskStatusDownloading = "downloading"
	TaskStatusInstalling  = "installing"
	// TaskStatusUninstalling is 'installing' for an uninstall deployment. It is a
	// separate value rather than a reuse of 'installing' so the console can say
	// what the endpoint is actually doing: an operator watching a rollback
	// progress needs to see it remove the package, not install it.
	TaskStatusUninstalling = "uninstalling"
	TaskStatusSuccess      = "success"
	TaskStatusFailed       = "failed"

	// TaskStatusFailedLost is terminal but distinct from TaskStatusFailed.
	//
	// The agent reports progress over HTTP from a goroutine, and an agent can
	// die at any point: a user logs off, the update engine replaces the running
	// binary mid-install, the machine is powered off, the network drops. When
	// that happens no report ever arrives and the task row would sit in
	// 'installing' forever, holding its parent deployment open and telling the
	// operator a rollout is still in progress after the last device finished
	// twenty minutes ago. The sweep moves those rows here.
	//
	// It is a separate status rather than 'failed' because the two mean
	// different things to whoever reads the rollup: 'failed' is an installer
	// that ran and rejected the operation, while 'failed_lost' is an installer
	// whose fate is genuinely unknown. The machine may be fully installed, may
	// be half-installed, or may have never started. The inventory sweep is what
	// settles it, and the operator re-deploys if it did not take.
	//
	// deployment_tasks.status carries no CHECK constraint, so adding a value
	// needs no migration.
	TaskStatusFailedLost = "failed_lost"
)

type SoftwarePackage struct {
	ID            string    `db:"id" json:"id"`
	Name          string    `db:"name" json:"name"`
	Version       string    `db:"version" json:"version"`
	OSTarget      string    `db:"os_target" json:"os_target"`
	PackageType   string    `db:"package_type" json:"package_type"`
	FileName      string    `db:"file_name" json:"file_name"`
	FileSize      int64     `db:"file_size" json:"file_size"`
	SHA256        string    `db:"sha256" json:"sha256"`
	StoragePath   string    `db:"storage_path" json:"-"`
	InstallArgs   string    `db:"install_args" json:"install_args"`
	UninstallArgs string    `db:"uninstall_args" json:"uninstall_args"`
	CreatedAt     time.Time `db:"created_at" json:"created_at"`
	UpdatedAt     time.Time `db:"updated_at" json:"updated_at"`
}

type SoftwareDeployment struct {
	ID          string     `db:"id" json:"id"`
	PackageID   string     `db:"package_id" json:"package_id"`
	Action      string     `db:"action" json:"action"`
	Name        string     `db:"name" json:"name"`
	TargetType  string     `db:"target_type" json:"target_type"`
	TargetID    string     `db:"target_id" json:"target_id"`
	CreatedBy   string     `db:"created_by" json:"created_by"`
	Status      string     `db:"status" json:"status"`
	CreatedAt   time.Time  `db:"created_at" json:"created_at"`
	CompletedAt *time.Time `db:"completed_at" json:"completed_at,omitempty"`

	// TaskArgs is not a column. It carries the argument list that the agent
	// will run, which CreateDeploymentTx copies onto every task it creates.
	// Keeping it off the struct is what stops it from being written as one.
	TaskArgs string `db:"-" json:"-"`

	// Enriched fields for API responses
	PackageName    string `db:"package_name" json:"package_name,omitempty"`
	PackageVersion string `db:"package_version" json:"package_version,omitempty"`
	TotalTasks     int    `db:"total_tasks" json:"total_tasks"`
	SuccessTasks   int    `db:"success_tasks" json:"success_tasks"`
	FailedTasks    int    `db:"failed_tasks" json:"failed_tasks"`
}

type DeploymentTask struct {
	ID           string     `db:"id" json:"id"`
	DeploymentID string     `db:"deployment_id" json:"deployment_id"`
	PackageID    string     `db:"package_id" json:"package_id"`
	DeviceID     string     `db:"device_id" json:"device_id"`
	Status       string     `db:"status" json:"status"`
	Args         string     `db:"args" json:"args"`
	ExitCode     *int       `db:"exit_code" json:"exit_code,omitempty"`
	OutputLog    *string    `db:"output_log" json:"output_log,omitempty"`
	ErrorMessage *string    `db:"error_message" json:"error_message,omitempty"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at" json:"updated_at"`
	CompletedAt  *time.Time `db:"completed_at" json:"completed_at,omitempty"`

	// Enriched fields
	Hostname string `db:"hostname" json:"hostname,omitempty"`
	Site     string `db:"site" json:"site,omitempty"`
}

type CreateDeploymentRequest struct {
	Name       string `json:"name"`
	PackageID  string `json:"package_id"`
	TargetType string `json:"target_type"` // 'device', 'group', 'all'
	TargetID   string `json:"target_id"`   // device_id or group_id (optional for 'all')
	// Action is 'install' or 'uninstall'. An empty value means install, which is
	// what every caller sent before uninstall existed.
	Action string `json:"action"`
}

type TaskProgressReport struct {
	TaskID       string  `json:"task_id"`
	Status       string  `json:"status"` // 'downloading', 'installing', 'success', 'failed'
	ExitCode     *int    `json:"exit_code,omitempty"`
	OutputLog    *string `json:"output_log,omitempty"`
	ErrorMessage *string `json:"error_message,omitempty"`
}

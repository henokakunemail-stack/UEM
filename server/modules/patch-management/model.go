package patchmgmt

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

const (
	SeverityCritical    = "critical"
	SeverityImportant   = "important"
	SeverityModerate    = "moderate"
	SeverityLow         = "low"
	SeverityUnspecified = "unspecified"

	CategorySecurity   = "security"
	CategoryCritical   = "critical"
	CategoryDefinition = "definition"
	CategoryUpdates    = "updates"
	CategoryFeature    = "feature"
	// CategoryDriver mirrors the agent constant. Kept in step with it because
	// both files enumerate the same vocabulary for the same column.
	CategoryDriver = "driver"

	StateMissing   = "missing"
	StateInstalled = "installed"
	StatePending   = "pending"

	JobStatusPending    = "pending"
	JobStatusDispatched = "dispatched"
	JobStatusInstalling = "installing"
	JobStatusCompleted  = "completed"
	JobStatusFailed     = "failed"

	RebootPolicyNoReboot       = "no_reboot"
	RebootPolicyRebootIfNeeded = "reboot_if_needed"
)

type DevicePatch struct {
	ID             string    `json:"id" db:"id"`
	DeviceID       string    `json:"device_id" db:"device_id"`
	PatchID        string    `json:"patch_id" db:"patch_id"`
	Title          string    `json:"title" db:"title"`
	Description    string    `json:"description" db:"description"`
	Severity       string    `json:"severity" db:"severity"`
	Category       string    `json:"category" db:"category"`
	KBID           string    `json:"kb_id" db:"kb_id"`
	SizeBytes      int64     `json:"size_bytes" db:"size_bytes"`
	InstalledState string    `json:"installed_state" db:"installed_state"`
	RebootRequired bool      `json:"reboot_required" db:"reboot_required"`
	DiscoveredAt   time.Time `json:"discovered_at" db:"discovered_at"`
	UpdatedAt      time.Time `json:"updated_at" db:"updated_at"`

	// Joined fields
	Hostname string `json:"hostname,omitempty" db:"hostname"`
	OSName   string `json:"os_name,omitempty" db:"os_name"`
}

type PatchInstallJob struct {
	ID             string     `json:"id" db:"id"`
	DeviceID       string     `json:"device_id" db:"device_id"`
	OperatorID     string     `json:"operator_id" db:"operator_id"`
	PatchIDs       string     `json:"patch_ids" db:"patch_ids"` // JSON string representation
	Status         string     `json:"status" db:"status"`
	RebootPolicy   string     `json:"reboot_policy" db:"reboot_policy"`
	RebootRequired bool       `json:"reboot_required" db:"reboot_required"`
	OutputLog      string     `json:"output_log" db:"output_log"`
	ErrorMessage   string     `json:"error_message" db:"error_message"`
	StartedAt      time.Time  `json:"started_at" db:"started_at"`
	CompletedAt    *time.Time `json:"completed_at" db:"completed_at"`

	// Joined fields
	OperatorName string `json:"operator_name,omitempty" db:"operator_name"`
	Hostname     string `json:"hostname,omitempty" db:"hostname"`
}

type FleetPatchSummary struct {
	TotalMissingPatches     int `json:"total_missing_patches"`
	CriticalSecurityPatches int `json:"critical_security_patches"`
	RebootPendingDevices    int `json:"reboot_pending_devices"`
	VulnerableDevices       int `json:"vulnerable_devices"`
}

type PatchScanReport struct {
	Patches []DevicePatch `json:"patches"`
}

type PatchInstallReport struct {
	JobID          string `json:"job_id"`
	Status         string `json:"status"`
	RebootRequired bool   `json:"reboot_required"`
	OutputLog      string `json:"output_log"`
	ErrorMessage   string `json:"error_message"`
}

type InstallRequest struct {
	PatchIDs     []string `json:"patch_ids"`
	RebootPolicy string   `json:"reboot_policy"`
}

func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

package patch

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
	// CategoryDriver marks a firmware or driver update. Control Panel lists
	// these under Windows Update, so leaving them out made a host with eight
	// pending driver upgrades report as compliant.
	CategoryDriver = "driver"

	StateMissing   = "missing"
	StateInstalled = "installed"
	StatePending   = "pending"
)

type PatchItem struct {
	PatchID        string `json:"patch_id"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Severity       string `json:"severity"`
	Category       string `json:"category"`
	KBID           string `json:"kb_id"`
	SizeBytes      int64  `json:"size_bytes"`
	InstalledState string `json:"installed_state"`
	RebootRequired bool   `json:"reboot_required"`
}

type InstallParams struct {
	JobID        string   `json:"job_id"`
	PatchIDs     []string `json:"patch_ids"`
	RebootPolicy string   `json:"reboot_policy"`
}

type InstallResult struct {
	JobID          string `json:"job_id"`
	Status         string `json:"status"`
	RebootRequired bool   `json:"reboot_required"`
	OutputLog      string `json:"output_log"`
	ErrorMessage   string `json:"error_message"`
}

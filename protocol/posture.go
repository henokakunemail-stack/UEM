package protocol

// Device security posture: what the agent observed about disk encryption,
// antivirus, firewall, local password policy and screen lock.
//
// It travels inside the inventory report as the "posture" key. An agent that
// predates it sends no key at all, and the server must show that as "unknown,
// agent does not report posture" -- never as compliant.
//
// Every control carries a State, and only one of three things may be said:
//
//   - PostureReported: the agent read real values; the detail fields hold them.
//   - PostureUnknown: the agent tried and could not tell; Reason says why.
//   - PostureNotApplicable: the control does not exist on this machine
//     (BitLocker on Windows Home); Reason says why.
//
// There is no "default safe" value. A collector that fails to read a control
// must say PostureUnknown, because a compliance report built on a guessed
// "on" is worse than one with a visible gap.
const (
	PostureReported      = "reported"
	PostureUnknown       = "unknown"
	PostureNotApplicable = "not_applicable"
)

// PostureSchemaVersion is bumped when a field changes meaning.
const PostureSchemaVersion = 1

// Posture is the full posture snapshot.
type Posture struct {
	SchemaVersion  int                   `json:"schema_version"`
	DiskEncryption DiskEncryptionPosture `json:"disk_encryption"`
	Antivirus      AntivirusPosture      `json:"antivirus"`
	Firewall       FirewallPosture       `json:"firewall"`
	PasswordPolicy PasswordPolicyPosture `json:"password_policy"`
	ScreenLock     ScreenLockPosture     `json:"screen_lock"`
	// LastBootUTC is RFC 3339; empty when unknown.
	LastBootUTC string `json:"last_boot_utc,omitempty"`
	// LoggedOnUsers are the interactive users signed in when collected.
	LoggedOnUsers []string `json:"logged_on_users,omitempty"`
	// BIOSReleaseDate is YYYY-MM-DD; empty when unknown. Used for asset age.
	BIOSReleaseDate string `json:"bios_release_date,omitempty"`
}

// DiskEncryptionPosture is BitLocker / LUKS / FileVault.
type DiskEncryptionPosture struct {
	State   string            `json:"state"`
	Reason  string            `json:"reason,omitempty"`
	Volumes []EncryptedVolume `json:"volumes,omitempty"`
}

// EncryptedVolume is one fixed volume.
type EncryptedVolume struct {
	Mount string `json:"mount"`
	// System is true for the OS volume (C: on Windows, / elsewhere).
	System bool `json:"system"`
	// Protected is true only when encryption is complete and protection is on.
	Protected bool   `json:"protected"`
	Method    string `json:"method,omitempty"`
	// PercentEncrypted is nil when the platform does not report progress.
	PercentEncrypted *float64 `json:"percent_encrypted,omitempty"`
	// Status is the platform's own wording ("FullyEncrypted", "On", ...).
	Status string `json:"status,omitempty"`
}

// AntivirusPosture lists what protection products are present.
type AntivirusPosture struct {
	State    string      `json:"state"`
	Reason   string      `json:"reason,omitempty"`
	Products []AVProduct `json:"products,omitempty"`
}

// AVProduct is one antivirus product. Pointer fields are nil when unknown.
type AVProduct struct {
	Name             string `json:"name"`
	Enabled          *bool  `json:"enabled,omitempty"`
	RealTime         *bool  `json:"real_time,omitempty"`
	UpToDate         *bool  `json:"up_to_date,omitempty"`
	SignatureAgeDays *int   `json:"signature_age_days,omitempty"`
}

// FirewallPosture is the host firewall, per profile on Windows.
type FirewallPosture struct {
	State    string            `json:"state"`
	Reason   string            `json:"reason,omitempty"`
	Profiles []FirewallProfile `json:"profiles,omitempty"`
}

// FirewallProfile is one profile (Domain/Private/Public), or the single
// firewall on Linux/macOS (Name is the product, e.g. "ufw").
type FirewallProfile struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// PasswordPolicyPosture is the local account password policy. Nil fields were
// not readable on this platform. MaxAgeDays 0 means passwords never expire and
// LockoutThreshold 0 means accounts are never locked out, which is how Windows
// itself encodes both.
type PasswordPolicyPosture struct {
	State            string `json:"state"`
	Reason           string `json:"reason,omitempty"`
	MinLength        *int   `json:"min_length,omitempty"`
	MaxAgeDays       *int   `json:"max_age_days,omitempty"`
	HistoryCount     *int   `json:"history_count,omitempty"`
	LockoutThreshold *int   `json:"lockout_threshold,omitempty"`
}

// ScreenLockPosture is the machine-wide screen lock enforced by policy.
type ScreenLockPosture struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// TimeoutSeconds is the enforced idle timeout; nil when none is enforced.
	TimeoutSeconds *int `json:"timeout_seconds,omitempty"`
	// RequirePassword is whether unlocking needs the password; nil when not
	// enforced machine-wide.
	RequirePassword *bool `json:"require_password,omitempty"`
}

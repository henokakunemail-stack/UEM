package devicemanagement

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Device is a registered endpoint. Mirrors the `devices` table.
// Pointer fields map to NULL-able columns. Every column the schema declares
// without NOT NULL must be a pointer here: modernc's driver refuses to scan a
// NULL into a non-pointer string or time.Time, which would turn a merely
// incomplete device row into a failed query.
type Device struct {
	ID                  string     `db:"id"`
	Hostname            string     `db:"hostname"`
	OSName              string     `db:"os_name"`
	OSVersion           *string    `db:"os_version"`
	AgentVersion        *string    `db:"agent_version"`
	Status              string     `db:"status"`
	LastSeenAt          *time.Time `db:"last_seen_at"`
	EnrolledAt          time.Time  `db:"enrolled_at"`
	EnrollmentTokenHash *string    `db:"enrollment_token_hash"` // NULL once consumed
	// EnrollmentTokenExpiresAt is when the pending token stops being honoured.
	// NULL means it does not expire, which is every row that predates the
	// column: those tokens were issued by a server that never enforced a
	// deadline, and stranding them would break an enrollment somebody chose.
	EnrollmentTokenExpiresAt *time.Time `db:"enrollment_token_expires_at"`
	DeviceSecretHash         string     `db:"device_secret_hash"`
	Site                     *string    `db:"site"`
	// RetiredAt is set when the device leaves the fleet. NULL means active.
	// The row is kept so audit references stay resolvable; the secret is cleared.
	RetiredAt *time.Time `db:"retired_at"`
	// Capabilities is the JSON array of command types the agent advertised in
	// its hello message, so the server never sends a command it would drop.
	// NULL until the agent first reconnects with a build that advertises them.
	Capabilities *string `db:"capabilities"`
	// LastPatchScanAt is when the agent last ran a patch scan. NULL means it has
	// never run one, which the console must say out loud rather than render as an
	// empty patch list -- a device that was never scanned and a device that is
	// fully patched both have zero pending rows.
	LastPatchScanAt *time.Time `db:"last_patch_scan_at"`
	CreatedAt       time.Time  `db:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at"`
}

const (
	StatusOnline  = "online"
	StatusOffline = "offline"
)

// OSVersionString, AgentVersionString, and SiteValue flatten the NULL-able
// columns to empty strings. A device that has enrolled but not yet reported a
// version genuinely has no value there, which is different from a scan failure.
func (d Device) OSVersionString() string {
	if d.OSVersion == nil {
		return ""
	}
	return *d.OSVersion
}

func (d Device) AgentVersionString() string {
	if d.AgentVersion == nil {
		return ""
	}
	return *d.AgentVersion
}

func (d Device) SiteValue() string {
	if d.Site == nil {
		return ""
	}
	return *d.Site
}

const (
	OSWindows = "windows"
	OSLinux   = "linux"
	OSMacOS   = "macos"
)

// NewID returns a cryptographically random hex ID (no external dep needed).
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// HashToken hashes a plaintext token/secret with SHA-256. Only the hash is stored.
func HashToken(plain string) string {
	h := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(h[:])
}

// GenerateToken returns a random URL-safe token.
func GenerateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

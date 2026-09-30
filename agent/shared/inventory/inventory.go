// Package inventory collects hardware and software facts about the machine the
// agent runs on. The collection types live in shared so every OS returns the
// same shape; each OS provides its own Collector behind a build tag.
package inventory

import "time"

// Hardware is the machine-level facts. Pointer/optional fields are those with no
// reliable source on some operating systems; senders must leave them nil rather
// than invent a value.
type Hardware struct {
	CPU CPU `json:"cpu"`
	// RAMTotalBytes is the physically installed memory, when the OS reports it.
	RAMTotalBytes int64  `json:"ram_total_bytes"`
	Disks         []Disk `json:"disks"`
	NICs          []NIC  `json:"nics"`
	Model         *Model `json:"model,omitempty"`
}

// CPU describes the processor(s).
type CPU struct {
	Name          string `json:"name"`
	NumberOfCores int    `json:"number_of_cores"`
	// LogicalProcessors is the count of executing threads the OS sees (SMT aware).
	LogicalProcessors int `json:"logical_processors"`
}

// Disk is one mounted volume.
type Disk struct {
	// Device/MountPoint naming differs per OS: drive letter on Windows, mount
	// path on Unix. Report raw; inventory normalises nothing.
	Name       string `json:"name"`
	Label      string `json:"label"`
	Filesystem string `json:"filesystem"`
	TotalBytes int64  `json:"total_bytes"`
	FreeBytes  int64  `json:"free_bytes"`
}

// NIC is one network interface.
type NIC struct {
	// Name is the OS-native name ("WiFi", "eth0"); it does not match across
	// operating systems. MAC is the stable cross-OS identifier.
	Name string   `json:"name"`
	MAC  string   `json:"mac"`
	MTU  int      `json:"mtu"`
	Up   bool     `json:"up"`
	IPs  []string `json:"ips"`
}

// Model is the chassis identity, when the vendor exposes it.
type Model struct {
	Vendor       string `json:"vendor"`
	Product      string `json:"product"`
	SerialNumber string `json:"serial_number"`
}

// Software is one installed program.
type Software struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Publisher string `json:"publisher"`
	// ProductCode is the MSI identity used to deduplicate entries that appear in
	// both the uninstall key and the per-user key. Empty when not applicable.
	ProductCode string `json:"product_code,omitempty"`
	InstallDate string `json:"install_date,omitempty"`
}

// OSDetail carries build/edition/lifecycle facts beyond osinfo's version string.
type OSDetail struct {
	Edition          string `json:"edition"`
	BuildNumber      string `json:"build_number"`
	Architecture     string `json:"architecture"`
	InstallDate      string `json:"install_date,omitempty"`
	LastBootUTC      string `json:"last_boot_utc,omitempty"`
	InstallTimestamp int64  `json:"install_timestamp,omitempty"`
}

// Report is the full collection result sent to the server.
type Report struct {
	Hardware Hardware   `json:"hardware"`
	Software []Software `json:"software"`
	OS       OSDetail   `json:"os"`
	// CollectedAt is when this snapshot was gathered, on the agent's clock. The
	// server stores it as the snapshot's authoritative time: the server cannot
	// know when an agent actually read the hardware, and an agent behind a NAT
	// may deliver a report long after collecting it.
	CollectedAt time.Time `json:"collected_at"`
}

// Collector gathers facts for the current OS. Implementations are build-tagged.
// Collect must be safe to call from a goroutine while the transport read loop is
// running; it must not block on network calls.
type Collector interface {
	Collect() (Report, error)
}

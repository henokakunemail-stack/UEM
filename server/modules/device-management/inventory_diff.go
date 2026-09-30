package devicemanagement

import (
	"encoding/json"
	"strconv"
	"strings"
)

// inventoryDiff describes what changed between two hardware/OS snapshots.
//
// The rule for significance: a change is audit-worthy when it changes what the
// machine *is*, not merely what is installed on it. A swapped CPU, added memory,
// a different chassis, or a changed disk count indicate re-provisioning,
// hardware theft, or virtual machine resizing — all things an administrator
// should see in the audit trail. A program updating to a new version does not.
type inventoryDiff struct {
	CPUChanged       bool
	RAMChanged       bool
	DiskCountChanged bool
	ModelChanged     bool
	OSBuildChanged   bool
}

// hwJSON mirrors the agent's inventory.Hardware for diffing.
type hwJSON struct {
	CPU struct {
		Name              string `json:"name"`
		NumberOfCores     int    `json:"number_of_cores"`
		LogicalProcessors int    `json:"logical_processors"`
	} `json:"cpu"`
	RAMTotalBytes int64 `json:"ram_total_bytes"`
	Disks         []struct {
		Name       string `json:"name"`
		TotalBytes int64  `json:"total_bytes"`
		FreeBytes  int64  `json:"free_bytes"`
	} `json:"disks"`
	Model *hwModel `json:"model"`
}

// osJSON mirrors the agent's inventory.OSDetail for diffing.
type osJSON struct {
	Edition          string `json:"edition"`
	BuildNumber      string `json:"build_number"`
	Architecture     string `json:"architecture"`
	InstallDate      string `json:"install_date,omitempty"`
	LastBootUTC      string `json:"last_boot_utc,omitempty"`
	InstallTimestamp int64  `json:"install_timestamp,omitempty"`
}

// diffInventory compares two hardware/OS snapshots.
func diffInventory(oldHW, newHW, oldOS, newOS string) inventoryDiff {
	var d inventoryDiff

	oh, nh := decodeHW(oldHW), decodeHW(newHW)

	d.CPUChanged = oh.CPU.Name != nh.CPU.Name ||
		oh.CPU.NumberOfCores != nh.CPU.NumberOfCores ||
		oh.CPU.LogicalProcessors != nh.CPU.LogicalProcessors
	d.RAMChanged = oh.RAMTotalBytes != nh.RAMTotalBytes
	d.DiskCountChanged = len(oh.Disks) != len(nh.Disks)
	d.ModelChanged = !modelsEqual(oh.Model, nh.Model)

	oo, no := decodeOS(oldOS), decodeOS(newOS)
	d.OSBuildChanged = oo.BuildNumber != no.BuildNumber

	return d
}

// softwareDiff computes which programs appeared or disappeared.
type softwareDiff struct {
	Added   []string
	Removed []string
	Changed bool
}

// diffSoftware compares two software snapshots. Programs are keyed by name: a
// version bump for an already-present program is not an add or a remove.
func diffSoftware(oldJSON, newJSON string) softwareDiff {
	var d softwareDiff
	oldSet := decodeSoftwareNames(oldJSON)
	newSet := decodeSoftwareNames(newJSON)

	for name := range newSet {
		if _, ok := oldSet[name]; !ok {
			d.Added = append(d.Added, name)
		}
	}
	for name := range oldSet {
		if _, ok := newSet[name]; !ok {
			d.Removed = append(d.Removed, name)
		}
	}
	d.Changed = len(d.Added) > 0 || len(d.Removed) > 0
	return d
}

// softwareEntry is one row of the agent's software JSON array.
type softwareEntry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// decodeSoftwareNames returns the set of program names in a software snapshot.
func decodeSoftwareNames(jsonStr string) map[string]struct{} {
	set := make(map[string]struct{})
	if jsonStr == "" {
		return set
	}
	var entries []softwareEntry
	if err := json.Unmarshal([]byte(jsonStr), &entries); err != nil {
		return set
	}
	for _, e := range entries {
		if e.Name != "" {
			set[e.Name] = struct{}{}
		}
	}
	return set
}

// auditWorthy returns true when the diff belongs in the audit trail rather than
// being recorded as ordinary drift.
func (d inventoryDiff) auditWorthy() bool {
	return d.CPUChanged || d.RAMChanged || d.DiskCountChanged || d.ModelChanged || d.OSBuildChanged
}

// summary renders a human-readable description for the audit log details field.
func (d inventoryDiff) summary(sw softwareDiff) string {
	var parts []string
	if d.CPUChanged {
		parts = append(parts, "cpu")
	}
	if d.RAMChanged {
		parts = append(parts, "memory")
	}
	if d.DiskCountChanged {
		parts = append(parts, "disk-count")
	}
	if d.ModelChanged {
		parts = append(parts, "model")
	}
	if d.OSBuildChanged {
		parts = append(parts, "os-build")
	}
	if len(sw.Added) > 0 {
		parts = append(parts, "software+"+strconv.Itoa(len(sw.Added)))
	}
	if len(sw.Removed) > 0 {
		parts = append(parts, "software-"+strconv.Itoa(len(sw.Removed)))
	}
	if len(parts) == 0 {
		return "no significant change"
	}
	return "changed: " + strings.Join(parts, ", ")
}

func decodeHW(s string) hwJSON {
	var h hwJSON
	if s == "" {
		return h
	}
	_ = json.Unmarshal([]byte(s), &h)
	return h
}

func decodeOS(s string) osJSON {
	var o osJSON
	if s == "" {
		return o
	}
	_ = json.Unmarshal([]byte(s), &o)
	return o
}

func modelsEqual(a, b *hwModel) bool {
	return hwModelValue(a) == hwModelValue(b)
}

// hwModel is the shape of inventory.Hardware.Model, repeated here so the diff
// code does not import the agent package (which would pull build tags into the
// server build).
type hwModel struct {
	Vendor       string `json:"vendor"`
	Product      string `json:"product"`
	SerialNumber string `json:"serial_number"`
}

// hwModelValue renders a model as a comparable string, treating nil and an empty
// model as equal so "not reported" does not diff against "reported empty".
func hwModelValue(m *hwModel) string {
	if m == nil {
		m = &hwModel{}
	}
	return m.Vendor + "|" + m.Product + "|" + m.SerialNumber
}

// decodeJSON is a helper for tests that need to parse an inventory section.
func decodeJSON(s string, v any) error {
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), v)
}

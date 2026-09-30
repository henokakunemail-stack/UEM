//go:build darwin

package macos

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
)

// macCollector implements inventory.Collector for macOS.
//
// Status: CODE COMPLETE (UNTESTED). No macOS runtime is available in this
// environment, so the sysctl/system_profiler parsers have never run against a
// real Mac.
type macCollector struct{}

// NewCollector returns the macOS inventory.Collector.
func NewCollector() inventory.Collector { return &macCollector{} }

func (c *macCollector) Collect() (inventory.Report, error) {
	var rep inventory.Report

	if hw, err := c.collectHardware(); err == nil {
		rep.Hardware = hw
	} else {
		rep.Hardware.NICs = inventory.CollectNICs()
	}
	rep.OS = c.collectOSDetail()
	rep.Software = inventory.DedupeSoftware(c.collectSoftware())
	return rep, nil
}

// macSysctl returns the value of one sysctl key, trimmed of its newline.
func macSysctl(key string) (string, error) {
	out, err := exec.Command("sysctl", "-n", key).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// macHardwareJSON is the subset of system_profiler SPHardwareDataType output
// that we parse for chassis identity.
type macHardwareJSON struct {
	SPHardwareDataType []struct {
		ModelName       string `json:"model_name"`
		ModelIdentifier string `json:"model_identifier"`
		SerialNumber    string `json:"serial_number"`
		ProcessorName   string `json:"processor_name"`
		TotalNumberCPUs int    `json:"total_number_of_cores"`
		Memory          string `json:"memory"`
	} `json:"SPHardwareDataType"`
}

// macProfileHardware runs system_profiler and parses its JSON output.
func macProfileHardware() (macHardwareJSON, error) {
	var parsed macHardwareJSON
	cmd := exec.Command("system_profiler", "SPHardwareDataType", "-json")
	raw, err := cmd.Output()
	if err != nil {
		return parsed, err
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return parsed, err
	}
	if len(parsed.SPHardwareDataType) == 0 {
		return parsed, fmt.Errorf("no SPHardwareDataType entries")
	}
	return parsed, nil
}

func (c *macCollector) collectHardware() (inventory.Hardware, error) {
	var hw inventory.Hardware
	hw.NICs = inventory.CollectNICs()
	hw.Disks = c.collectDisks()

	prof, err := macProfileHardware()
	if err == nil {
		h := prof.SPHardwareDataType[0]
		hw.CPU = inventory.CPU{Name: h.ProcessorName, NumberOfCores: h.TotalNumberCPUs}
		hw.Model = &inventory.Model{
			Vendor:       "Apple",
			Product:      firstNonEmpty(h.ModelName, h.ModelIdentifier),
			SerialNumber: h.SerialNumber,
		}
		if kb, err := macParseMemory(h.Memory); err == nil {
			hw.RAMTotalBytes = kb * 1024
		}
	}

	// Sysctl as fallback or refinement for CPU/logical processor counts.
	if hw.CPU.LogicalProcessors == 0 {
		if n, err := macSysctl("hw.logicalcpu"); err == nil {
			if i, err := strconv.Atoi(n); err == nil {
				hw.CPU.LogicalProcessors = i
			}
		}
	}
	if hw.CPU.NumberOfCores == 0 {
		if n, err := macSysctl("hw.physicalcpu"); err == nil {
			if i, err := strconv.Atoi(n); err == nil {
				hw.CPU.NumberOfCores = i
			}
		}
	}
	return hw, nil
}

// macParseMemory converts a system_profiler memory string ("8 GB", "16 GB") to
// kibibytes, which is the unit hw.memsize uses.
func macParseMemory(s string) (int64, error) {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return 0, fmt.Errorf("unexpected memory string %q", s)
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	unit := strings.ToLower(fields[1])
	switch unit {
	case "gb":
		return int64(n * 1024 * 1024), nil
	case "mb":
		return int64(n * 1024), nil
	case "tb":
		return int64(n * 1024 * 1024 * 1024), nil
	}
	return 0, fmt.Errorf("unknown memory unit %q", unit)
}

// collectDisks reports mounted volumes. macOS uses statfs with a different
// struct name but the same semantics as Linux.
func (c *macCollector) collectDisks() []inventory.Disk {
	out, err := exec.Command("mount").Output()
	if err != nil {
		return nil
	}
	var disks []inventory.Disk
	for _, line := range strings.Split(string(out), "\n") {
		// macOS mount output: "/dev/disk1s1 on /Volumes/My Drive (apfs, local, journaled)"
		// The mountpoint sits between " on " and the last " (" before the options.
		onIdx := strings.Index(line, " on ")
		if onIdx < 0 {
			continue
		}
		rest := line[onIdx+4:] // everything after " on "
		// Find the options parenthesis — it's the last " (" in the remainder.
		parenIdx := strings.LastIndex(rest, " (")
		if parenIdx < 0 {
			continue
		}
		mountpoint := rest[:parenIdx]

		var st syscall.Statfs_t
		if err := syscall.Statfs(mountpoint, &st); err != nil {
			continue
		}
		disks = append(disks, inventory.Disk{
			Name:       mountpoint,
			TotalBytes: int64(st.Blocks) * int64(st.Bsize),
			FreeBytes:  int64(st.Bavail) * int64(st.Bsize),
		})
	}
	return disks
}

// collectOSDetail reads sw_vers output for name and version.
func (c *macCollector) collectOSDetail() inventory.OSDetail {
	var osd inventory.OSDetail
	out, err := exec.Command("sw_vers").Output()
	if err != nil {
		return osd
	}
	for _, line := range strings.Split(string(out), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "ProductName":
			osd.Edition = val
		case "ProductVersion":
			osd.BuildNumber = val
		}
	}
	if arch, err := macSysctl("hw.machine"); err == nil {
		osd.Architecture = arch
	}
	return osd
}

// collectSoftware lists .app bundles and their Info.plist versions. Applications
// outside /Applications (e.g. /System/Applications) are included because they are
// what an admin comparing fleets needs to see.
func (c *macCollector) collectSoftware() []inventory.Software {
	var out []inventory.Software

	for _, root := range []string{"/Applications", "/System/Applications"} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue // /System/Applications may be unreadable or absent
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.HasSuffix(e.Name(), ".app") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".app")
			plist := filepath.Join(root, e.Name(), "Contents", "Info.plist")
			if s, ok := macReadAppInfo(plist, name); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// macReadAppInfo reads CFBundleShortVersionString and CFBundleIdentifier from an
// app's Info.plist. The plist is binary on modern macOS, so plutil converts it.
func macReadAppInfo(plistPath, name string) (inventory.Software, bool) {
	// Binary plists need plutil; fall back to nothing if unavailable.
	if _, err := os.Stat(plistPath); err != nil {
		return inventory.Software{}, false
	}

	cmd := exec.Command("plutil", "-convert", "json", "-o", "-", plistPath)
	raw, err := cmd.Output()
	if err != nil {
		return inventory.Software{}, false
	}
	var info struct {
		BundleID     string `json:"CFBundleIdentifier"`
		BundleName   string `json:"CFBundleName"`
		ShortVersion string `json:"CFBundleShortVersionString"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return inventory.Software{}, false
	}
	display := info.BundleName
	if display == "" {
		display = name
	}
	return inventory.Software{
		Name:    display,
		Version: info.ShortVersion,
		// CFBundleIdentifier is the closest thing macOS has to a product code.
		ProductCode: info.BundleID,
	}, true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

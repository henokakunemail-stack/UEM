//go:build linux

package linux

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
)

// linuxCollector implements inventory.Collector for Linux.
//
// Status: CODE COMPLETE (UNTESTED). No Linux runtime is available in this
// environment, so these parsers have been written against the documented formats
// of /proc and /sys but never executed against a real system.
type linuxCollector struct{}

// NewCollector returns the Linux inventory.Collector.
func NewCollector() inventory.Collector { return &linuxCollector{} }

func (c *linuxCollector) Collect() (inventory.Report, error) {
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

// linuxProcCPUInfo models the fields we read from /proc/cpuinfo.
type linuxProcCPUInfo struct {
	ModelName string
	// Processors counts physical processor entries. SMT siblings are listed as
	// separate "processor" lines, so this is the logical count.
	Processors int
}

// readCPUInfo parses /proc/cpuinfo. The file layout differs subtly across
// architectures (x86 says "model name", ARM says "Processor"), so both are read.
func readCPUInfo() (linuxProcCPUInfo, error) {
	var info linuxProcCPUInfo
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return info, err
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 4096), 256*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "model name", "Processor", "cpu model":
			if info.ModelName == "" {
				info.ModelName = val
			}
		case "processor":
			info.Processors++
		}
	}
	return info, s.Err()
}

func (c *linuxCollector) collectHardware() (inventory.Hardware, error) {
	var hw inventory.Hardware
	hw.NICs = inventory.CollectNICs()
	hw.Disks = c.collectDisks()

	if cpu, err := c.collectCPU(); err == nil {
		hw.CPU = cpu
	}
	if ram, err := c.collectRAM(); err == nil {
		hw.RAMTotalBytes = ram
	}
	hw.Model = c.collectModel()
	return hw, nil
}

func (c *linuxCollector) collectCPU() (inventory.CPU, error) {
	var cpu inventory.CPU
	info, err := readCPUInfo()
	if err != nil {
		return cpu, err
	}
	cpu.Name = info.ModelName
	cpu.LogicalProcessors = info.Processors

	// Physical cores: /sys/devices/system/cpu/cpu*/topology/core_id gives one
	// entry per logical processor; distinct values are physical cores. Absent on
	// some kernels/containers, in which case cores stay unreported rather than
	// guessed.
	seen := make(map[string]struct{})
	entries, err := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/topology/core_id")
	if err == nil {
		for _, e := range entries {
			b, err := os.ReadFile(e)
			if err != nil {
				continue
			}
			seen[strings.TrimSpace(string(b))] = struct{}{}
		}
		if len(seen) > 0 {
			cpu.NumberOfCores = len(seen)
		}
	}
	return cpu, nil
}

// collectRAM reads MemTotal from /proc/meminfo. The value there is in kibibytes.
func (c *linuxCollector) collectRAM() (int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line) // ["MemTotal:", "16287648", "kB"]
		if len(fields) < 2 {
			return 0, fmt.Errorf("unexpected MemTotal line: %s", line)
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("MemTotal not found")
}

// linuxPseudoFs is the set of filesystem types never reported as real disks.
var linuxPseudoFs = map[string]bool{
	"proc": true, "sysfs": true, "cgroup": true, "cgroup2": true,
	"tmpfs": true, "devtmpfs": true, "devpts": true, "mqueue": true,
	"debugfs": true, "tracefs": true, "securityfs": true, "bpf": true,
	"pstore": true, "configfs": true, "fusectl": true, "autofs": true,
	"binfmt_misc": true, "ramfs": true, "overlay": true, "nsfs": true,
}

// collectDisks reports real mounted filesystems. /proc/mounts is the source of
// truth for what is actually mounted; pseudo filesystems are filtered out.
func (c *linuxCollector) collectDisks() []inventory.Disk {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()

	var disks []inventory.Disk
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 4096), 256*1024)
	for s.Scan() {
		// device mountpoint fstype opts dump pass
		fields := strings.Fields(s.Text())
		if len(fields) < 3 {
			continue
		}
		mountpoint, fstype := fields[1], fields[2]
		if linuxPseudoFs[fstype] {
			continue
		}
		// Unescape the octal escapes /proc/mounts uses for spaces etc.
		mountpoint = linuxUnescapeMount(mountpoint)

		var st syscall.Statfs_t
		if err := syscall.Statfs(mountpoint, &st); err != nil {
			continue
		}
		disks = append(disks, inventory.Disk{
			Name:       mountpoint,
			Filesystem: fstype,
			TotalBytes: int64(st.Blocks) * st.Bsize,
			FreeBytes:  int64(st.Bavail) * st.Bsize,
		})
	}
	return disks
}

// linuxUnescapeMount decodes the \040 style escapes /proc/mounts uses.
func linuxUnescapeMount(s string) string {
	if !strings.Contains(s, `\0`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseInt(s[i+1:i+4], 8, 32); err == nil {
				b.WriteRune(rune(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// collectModel reads the DMI sysfs attributes. These are readable on most
// firmware systems but absent in containers, where nil is the honest answer.
func (c *linuxCollector) collectModel() *inventory.Model {
	vendor, _ := os.ReadFile("/sys/class/dmi/id/board_vendor")
	product, _ := os.ReadFile("/sys/class/dmi/id/product_name")
	serial, _ := os.ReadFile("/sys/class/dmi/id/product_serial")

	m := &inventory.Model{
		Vendor:       strings.TrimSpace(string(vendor)),
		Product:      strings.TrimSpace(string(product)),
		SerialNumber: strings.TrimSpace(string(serial)),
	}
	if m.Vendor == "" && m.Product == "" && m.SerialNumber == "" {
		return nil
	}
	return m
}

// collectOSDetail reads /etc/os-release for the distribution name and version.
// There is no reliable cross-distro install-date source, so it is omitted rather
// than approximated.
func (c *linuxCollector) collectOSDetail() inventory.OSDetail {
	var osd inventory.OSDetail
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return osd
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		key, val, ok := strings.Cut(strings.TrimSpace(s.Text()), "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, `"`)
		switch key {
		case "PRETTY_NAME":
			osd.Edition = val
		case "VERSION_ID":
			osd.BuildNumber = val
		}
	}

	var u syscall.Utsname
	if syscall.Uname(&u) == nil {
		osd.Architecture = utsString(u.Machine)
	}
	return osd
}

// utsString converts a fixed-size [65]int8 array from uname to a Go string.
// syscall.Utsname fields are [65]int8 on all current Linux GOARCH values
// (verified amd64 and arm64 in Go 1.26). The int8â†’byte cast is safe because
// the values are ASCII characters (0-127).
func utsString(b [65]int8) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}

// collectSoftware reports installed packages. dpkg is parsed from its status
// file (no exec, fast); rpm requires the rpm binary; both are attempted so a
// mixed-OS fleet still reports something on each distro.
func (c *linuxCollector) collectSoftware() []inventory.Software {
	out := make([]inventory.Software, 0, 256)
	out = append(out, parseDpkgStatus()...)
	out = append(out, queryRpm()...)
	return out
}

// parseDpkgStatus reads /var/lib/dpkg/status. It is a paragraph format with
// Package/Version fields; Description carries the summary.
func parseDpkgStatus() []inventory.Software {
	f, err := os.Open("/var/lib/dpkg/status")
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []inventory.Software
	var cur inventory.Software
	have := false
	installed := false // tracks whether the package's Status says "installed"

	flush := func() {
		if have && cur.Name != "" && installed {
			out = append(out, cur)
		}
		cur, have, installed = inventory.Software{}, false, false
	}

	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 8192), 4*1024*1024)
	for s.Scan() {
		line := s.Text()
		if line == "" {
			flush()
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "Package":
			cur.Name = val
			have = true
		case "Version":
			cur.Version = val
		case "Maintainer":
			cur.Publisher = val
		case "Status":
			// dpkg status format: "want flag status", e.g. "install ok installed".
			// Only report packages whose status ends with "installed".
			installed = strings.HasSuffix(val, " installed")
		}
	}
	flush()
	return out
}

// queryRpm lists RPM packages. This needs the rpm binary, which is only present
// on rpm-based distros; where it is missing, no entries are returned.
func queryRpm() []inventory.Software {
	cmd := exec.Command("rpm", "-qa", "--qf",
		"%{NAME}\t%{VERSION}-%{RELEASE}\t%{VENDOR}\n")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var pkgs []inventory.Software
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		p := inventory.Software{Name: fields[0], Version: fields[1]}
		if len(fields) > 2 {
			p.Publisher = fields[2]
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

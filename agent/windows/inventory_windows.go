//go:build windows

package windows

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
)

// winCollector implements inventory.Collector for Windows.
//
// Data sources, chosen after verifying each API exists in
// golang.org/x/sys@v0.48.0 (phase-2 plan Â§1.6):
//   - software list: registry Uninstall keys (fast, reliable, same source as
//     the "Programs and Features" control panel)
//   - disk volumes:  GetLogicalDriveStrings + GetVolumeInformation + GetDiskFreeSpaceEx
//   - RAM + chassis: Get-CimInstance via PowerShell. GlobalMemoryStatusEx is NOT
//     exported by golang.org/x/sys and GetSystemFirmwareTable is absent, so WMI
//     through CIM is the only pure-Go-reachable path.
//   - CPU name:      registry ProcessorNameString; CPU count: GetActiveProcessorCount
type winCollector struct{}

// NewCollector returns the Windows inventory.Collector.
func NewCollector() inventory.Collector { return &winCollector{} }

// winUninstallSubKeys are the standard uninstall locations. The WoW64 node is
// listed so a 32-bit program on 64-bit Windows still appears once.
var winUninstallSubKeys = []string{
	`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
	`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
}

func (c *winCollector) Collect() (inventory.Report, error) {
	var rep inventory.Report

	// Software first: cheapest source and the one most likely to succeed.
	rep.Software = inventory.DedupeSoftware(c.collectSoftware())

	if osd, err := c.collectOSDetail(); err == nil {
		rep.OS = osd
	}
	if hw, err := c.collectHardware(); err == nil {
		rep.Hardware = hw
	} else {
		// Best effort: a failed CIM exec must not void software and OS detail,
		// which are valid inventory on their own.
		rep.Hardware.NICs = inventory.CollectNICs()
	}
	return rep, nil
}

// collectSoftware reads registry Uninstall keys and returns installed programs.
func (c *winCollector) collectSoftware() []inventory.Software {
	out := make([]inventory.Software, 0, 256)
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, sub := range winUninstallSubKeys {
			k, err := registry.OpenKey(root, sub, registry.READ|registry.ENUMERATE_SUB_KEYS)
			if err != nil {
				continue // per-user key is absent for SYSTEM/service accounts
			}
			names, err := k.ReadSubKeyNames(-1)
			k.Close()
			if err != nil {
				continue
			}
			for _, name := range names {
				if s, ok := c.readUninstallEntry(root, sub+`\`+name); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// readUninstallEntry reads one Uninstall subkey. Returns ok=false for entries
// that are not user-visible programs: entries without a DisplayName are usually
// language packs or servicing payloads.
func (c *winCollector) readUninstallEntry(root registry.Key, path string) (inventory.Software, bool) {
	k, err := registry.OpenKey(root, path, registry.READ)
	if err != nil {
		return inventory.Software{}, false
	}
	defer k.Close()

	name, _, err := k.GetStringValue("DisplayName")
	if err != nil || strings.TrimSpace(name) == "" {
		return inventory.Software{}, false
	}
	version, _, _ := k.GetStringValue("DisplayVersion")
	publisher, _, _ := k.GetStringValue("Publisher")
	productCode, _, _ := k.GetStringValue("ProductCode")
	installDate, _, _ := k.GetStringValue("InstallDate") // "YYYYMMDD" in ARP entries

	return inventory.Software{
		Name:        strings.TrimSpace(name),
		Version:     strings.TrimSpace(version),
		Publisher:   strings.TrimSpace(publisher),
		ProductCode: strings.TrimSpace(productCode),
		InstallDate: installDate,
	}, true
}

// collectOSDetail gathers build/edition/architecture facts. RtlGetVersion reports
// the real kernel version rather than the version the application manifest claims
// (a Phase 1 finding).
func (c *winCollector) collectOSDetail() (inventory.OSDetail, error) {
	var osd inventory.OSDetail

	v := windows.RtlGetVersion()
	if v == nil {
		return osd, fmt.Errorf("RtlGetVersion returned nil")
	}
	osd.BuildNumber = fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)

	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.READ)
	if err != nil {
		return osd, err
	}
	defer k.Close()

	if edition, _, err := k.GetStringValue("EditionID"); err == nil {
		osd.Edition = strings.TrimSpace(edition)
	}
	if arch, _, err := k.GetStringValue("CurrentArchitecture"); err == nil {
		osd.Architecture = strings.TrimSpace(arch)
	}
	if osd.Architecture == "" {
		osd.Architecture = archName() // process bitness: accurate for a 64-bit agent
	}
	// InstallDate in this key is a Unix timestamp written by setup.
	if ts, _, err := k.GetIntegerValue("InstallDate"); err == nil {
		osd.InstallTimestamp = int64(ts)
	}
	return osd, nil
}

// archName reports the CPU architecture of the running process.
func archName() string {
	switch unsafe.Sizeof(uintptr(0)) {
	case 8:
		return "x64"
	case 4:
		return "x86"
	default:
		return "unknown"
	}
}

// collectHardware gathers CPU, RAM, disks, NICs and chassis identity.
func (c *winCollector) collectHardware() (inventory.Hardware, error) {
	var hw inventory.Hardware
	hw.NICs = inventory.CollectNICs()
	hw.Disks = c.collectDisks()
	if cpu, err := c.collectCPU(); err == nil {
		hw.CPU = cpu
	}
	if ram, err := c.collectRAM(); err == nil {
		hw.RAMTotalBytes = ram
	} else {
		// Memory is a headline dashboard fact, so its absence is worth a log line
		// even though the rest of the collection is still usable.
		log.Warn().Err(err).Msg("inventory: ram unavailable")
	}
	hw.Model = c.collectModel()
	return hw, nil
}

// collectCPU reads the processor name from the registry and the logical processor
// count from the native API.
func (c *winCollector) collectCPU() (inventory.CPU, error) {
	var cpu inventory.CPU
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.READ)
	if err != nil {
		return cpu, err
	}
	defer k.Close()

	name, _, err := k.GetStringValue("ProcessorNameString")
	if err != nil {
		return cpu, err
	}
	cpu.Name = strings.TrimSpace(name)
	cpu.LogicalProcessors = int(windows.GetActiveProcessorCount(windows.ALL_PROCESSOR_GROUPS))

	// Physical core count needs GetLogicalProcessorInformation, which x/sys does
	// not export. Rather than report a plausible-looking wrong number, leave
	// cores at 0 and let the console show "not reported".
	cpu.NumberOfCores = 0
	return cpu, nil
}

// collectDisks enumerates mounted volumes and their free space.
func (c *winCollector) collectDisks() []inventory.Disk {
	var buf [512]uint16
	n, err := windows.GetLogicalDriveStrings(uint32(len(buf)), &buf[0])
	if err != nil || n == 0 {
		return nil
	}
	// Result is "C:\\\0D:\\\0\0"; split on NUL and drop the terminating empties.
	drives := strings.Split(strings.TrimRight(windows.UTF16ToString(buf[:n]), "\x00"), "\x00")

	disks := make([]inventory.Disk, 0, len(drives))
	for _, d := range drives {
		if d == "" {
			continue
		}
		if disk, ok := c.collectDisk(d); ok {
			disks = append(disks, disk)
		}
	}
	return disks
}

// collectDisk reports one volume. A drive letter with no media (an empty optical
// drive, for instance) returns ok=false and is skipped.
func (c *winCollector) collectDisk(rootPath string) (inventory.Disk, bool) {
	root, err := windows.UTF16PtrFromString(rootPath)
	if err != nil {
		return inventory.Disk{}, false
	}

	var freeCaller, total, freeTotal uint64
	if err := windows.GetDiskFreeSpaceEx(root, &freeCaller, &total, &freeTotal); err != nil {
		return inventory.Disk{}, false
	}

	var volName, fsName [256]uint16
	var maxComponent, fsFlags uint32
	err = windows.GetVolumeInformation(root, &volName[0], uint32(len(volName)), nil,
		&maxComponent, &fsFlags, &fsName[0], uint32(len(fsName)))

	disk := inventory.Disk{
		Name:       rootPath,
		TotalBytes: int64(total),
		FreeBytes:  int64(freeCaller),
	}
	if err == nil {
		disk.Label = windows.UTF16ToString(volName[:])
		disk.Filesystem = windows.UTF16ToString(fsName[:])
	}
	return disk, true
}

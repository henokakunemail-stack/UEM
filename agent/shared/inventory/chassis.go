package inventory

import (
	"strings"
	"time"
)

// Chassis types as the server's asset lifecycle reads them.
const (
	ChassisLaptop  = "laptop"
	ChassisDesktop = "desktop"
	ChassisServer  = "server"
)

// ChassisFromPCSystemType maps Win32_ComputerSystem.PCSystemType. 0
// (unspecified), 6 (appliance) and anything unknown give "", which the
// server shows as an unclassified machine rather than a guess.
func ChassisFromPCSystemType(n int64) string {
	switch n {
	case 1, 3: // Desktop, Workstation
		return ChassisDesktop
	case 2, 8: // Mobile, Slate
		return ChassisLaptop
	case 4, 5, 7: // Enterprise Server, SOHO Server, Performance Server
		return ChassisServer
	}
	return ""
}

// ChassisFromSMBIOS maps the SMBIOS chassis type (Linux
// /sys/class/dmi/id/chassis_type). Virtual machines usually report 1
// ("Other"), which stays "".
func ChassisFromSMBIOS(n int) string {
	switch n {
	case 3, 4, 5, 6, 7, 13, 15, 16, 24, 35, 36:
		// Desktop, Low Profile Desktop, Pizza Box, Mini Tower, Tower, All in
		// One, Space-saving, Lunch Box, Sealed-case PC, Mini PC, Stick PC
		return ChassisDesktop
	case 8, 9, 10, 14, 30, 31, 32:
		// Portable, Laptop, Notebook, Sub Notebook, Tablet, Convertible,
		// Detachable
		return ChassisLaptop
	case 17, 23, 28:
		// Main Server Chassis, Rack Mount Chassis, Blade
		return ChassisServer
	}
	return ""
}

// ChassisFromMacModel reads the model name system_profiler reports
// ("MacBook Pro", "Mac mini", "iMac", "Mac Pro", "Mac Studio").
func ChassisFromMacModel(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case n == "":
		return ""
	case strings.HasPrefix(n, "macbook"):
		return ChassisLaptop
	case strings.HasPrefix(n, "imac"), strings.HasPrefix(n, "mac mini"),
		strings.HasPrefix(n, "mac pro"), strings.HasPrefix(n, "mac studio"):
		return ChassisDesktop
	}
	return ""
}

// ParseDMIBIOSDate turns the DMI bios_date ("MM/DD/YYYY") into YYYY-MM-DD,
// or "" when it is not a date.
func ParseDMIBIOSDate(s string) string {
	t, err := time.Parse("01/02/2006", strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02")
}

//go:build windows

package windows

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
)

// cimQuery runs a Get-CimInstance query and returns the rows as parsed JSON.
//
// wmic.exe is gone from Windows 11, so CIM through PowerShell is the only
// command-line WMI path left, and GlobalMemoryStatusEx is not exported by
// golang.org/x/sys — together those two facts are why RAM and chassis identity
// arrive through this exec rather than a native API.
func cimQuery(ctx context.Context, class string, props []string) ([]map[string]any, error) {
	// Ask for JSON output: PowerShell's default text formatting of objects
	// truncates long values and is not reliably parseable.
	list := strings.Join(props, ",")
	script := fmt.Sprintf(
		"Get-CimInstance -ClassName %s -Property %s | Select-Object %s | ConvertTo-Json -Compress -Depth 4",
		class, list, list)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-Command", script)

	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cim %s: %w", class, err)
	}
	return parseCIMRows(raw)
}

// parseCIMRows decodes the bytes ConvertTo-Json emits. It is the parsing half of
// cimQuery, factored out so it can be unit tested without powershell.exe.
func parseCIMRows(raw []byte) ([]map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("cim: empty output")
	}
	// With -Compress, ConvertTo-Json has always emitted a JSON array here, even
	// for a single row. A bare object is still accepted: it is the shape the
	// cmdlet documents, and a future PowerShell build that drops the wrapper
	// would otherwise turn every single-row CIM class into "0 bytes RAM" with no
	// error to follow. The wrapped buffer is fresh because trimmed may alias raw.
	if trimmed[0] == '{' {
		wrapped := make([]byte, 0, len(trimmed)+2)
		wrapped = append(wrapped, '[')
		wrapped = append(wrapped, trimmed...)
		wrapped = append(wrapped, ']')
		trimmed = wrapped
	}
	var rows []map[string]any
	if err := json.Unmarshal(trimmed, &rows); err != nil {
		return nil, fmt.Errorf("cim: parse: %w", err)
	}
	return rows, nil
}

// asInt64 coerces a CIM value to int64. PowerShell emits numbers as JSON
// floats; sometimes they arrive as strings instead.
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case string:
		var i int64
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
			return i, true
		}
	}
	return 0, false
}

// collectRAM reads total physical memory via Win32_ComputerSystem.
//
// GlobalMemoryStatusEx and GetPhysicallyInstalledSystemMemory are not exported
// by golang.org/x/sys, and wmic.exe is gone from Windows 11, so CIM through
// PowerShell is the only pure-Go-reachable source. An error here propagates so
// the log line says why memory is missing rather than silently reporting 0.
func (c *winCollector) collectRAM() (int64, error) {
	rows, err := cimQuery(context.Background(), "Win32_ComputerSystem",
		[]string{"TotalPhysicalMemory"})
	if err != nil {
		return 0, fmt.Errorf("collect ram: %w", err)
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("collect ram: Win32_ComputerSystem returned no rows")
	}
	if n, ok := asInt64(rows[0]["TotalPhysicalMemory"]); ok {
		return n, nil
	}
	return 0, fmt.Errorf("collect ram: unexpected RAM type %T",
		rows[0]["TotalPhysicalMemory"])
}

// collectModel reads chassis identity via Win32_ComputerSystem and Win32_BIOS.
// Returns nil when nothing is available, so the console can show "not reported"
// instead of a fabricated value.
func (c *winCollector) collectModel() *inventory.Model {
	cs, err := cimQuery(context.Background(), "Win32_ComputerSystem",
		[]string{"Manufacturer", "Model", "PCSystemType"})
	if err != nil || len(cs) == 0 {
		return nil
	}
	m := &inventory.Model{}
	if s, ok := cs[0]["Manufacturer"].(string); ok {
		m.Vendor = strings.TrimSpace(s)
	}
	if s, ok := cs[0]["Model"].(string); ok {
		m.Product = strings.TrimSpace(s)
	}
	if n, ok := asInt64(cs[0]["PCSystemType"]); ok {
		m.ChassisType = inventory.ChassisFromPCSystemType(n)
	}

	if bios, err := cimQuery(context.Background(), "Win32_BIOS",
		[]string{"SerialNumber"}); err == nil && len(bios) > 0 {
		if s, ok := bios[0]["SerialNumber"].(string); ok {
			m.SerialNumber = strings.TrimSpace(s)
		}
	}

	if m.Vendor == "" && m.Product == "" && m.SerialNumber == "" {
		return nil
	}
	return m
}

//go:build windows

package software

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// winUninstallSubKeys are the two standard locations. The WOW6432Node mirror is
// not optional: a 32-bit program on 64-bit Windows registers only there, so
// without it half the installed programs are invisible to an uninstall.
var winUninstallSubKeys = []string{
	`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
	`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
}

// findInstalled returns every program on this machine whose name matches.
//
// A registry survey of a real workstation found 76 uninstall entries, of which
// 25 are msiexec-driven and only 8 carry a QuietUninstallString. So the search
// is name-based across all three registry locations and the caller decides what
// to do about more than one hit; guessing which program an operator meant is
// the one failure mode this feature cannot have.
func findInstalled(ctx context.Context, name string) ([]*installedProgram, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	want := normalizeProgramName(name)

	var out []*installedProgram
	seen := make(map[string]bool)

	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		for _, sub := range winUninstallSubKeys {
			k, err := registry.OpenKey(root, sub, registry.READ|registry.ENUMERATE_SUB_KEYS)
			if err != nil {
				continue // the per-user key is absent for a service account
			}
			names, err := k.ReadSubKeyNames(-1)
			k.Close()
			if err != nil {
				continue
			}
			for _, keyName := range names {
				p := readUninstallProgram(root, sub+`\`+keyName)
				if p == nil {
					continue
				}
				if !nameMatches(want, p.Name) {
					continue
				}
				// A program registered in both the 32- and 64-bit views is one
				// program. Its key name is stable across both, so it is the
				// deduplication key.
				id := keyName + "\x00" + p.ProductCode
				if seen[id] {
					continue
				}
				seen[id] = true
				out = append(out, p)
			}
		}
	}
	return out, nil
}

func readUninstallProgram(root registry.Key, path string) *installedProgram {
	k, err := registry.OpenKey(root, path, registry.READ)
	if err != nil {
		return nil
	}
	defer k.Close()

	get := func(name string) string {
		v, _, err := k.GetStringValue(name)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(v)
	}

	display := get("DisplayName")
	if display == "" {
		// Entries without a name are language packs and servicing payloads.
		return nil
	}
	uninstStr := get("UninstallString")
	framework := ""
	if get("Inno Setup: Setup Version") != "" || get("Inno Setup Code") != "" || strings.HasSuffix(strings.ToLower(path), "_is1") {
		framework = "inno"
	} else {
		base := strings.ToLower(filepath.Base(uninstallerPath(uninstStr)))
		if isInnoExeName(base) {
			framework = "inno"
		} else if base == "uninstall.exe" || base == "uninst.exe" {
			framework = "nsis"
		}
	}

	return &installedProgram{
		Name:            display,
		Version:         get("DisplayVersion"),
		Publisher:       get("Publisher"),
		ProductCode:     get("ProductCode"),
		UninstallString: uninstStr,
		QuietString:     get("QuietUninstallString"),
		Framework:       framework,
	}
}

func runPlatformUninstall(ctx context.Context, p *installedProgram, args []string) (int, string, error) {
	// The MSI tier is the only one on Windows that works unattended with no
	// operator input at all: msiexec knows how to uninstall quietly, and the
	// ProductCode is in the recorded command. Of the 76 entries surveyed on a
	// real machine, 25 are this shape.
	if isMsiUninstall(p.UninstallString) {
		return runMsiUninstall(ctx, p, args)
	}

	// Everything else is a program's own uninstaller, reached through the path
	// its installer recorded. There is no universal silent switch: NSIS wants
	// /S, Inno Setup wants /VERYSILENT /SUPPRESSMSGBOXES /NORESTART, and WinRAR
	// wants /s. A registry survey found no installer-framework signature in
	// WinRAR's uninstall.exe at all, which is why the operator supplies the
	// switch rather than the agent inferring one.
	path, err := resolveUninstallerPath(uninstallerPath(p.UninstallString))
	if err != nil {
		return -1, "", err
	}
	res := runProcess(ctx, path, args)
	return res.exitCode, decodeOutput([]byte(res.output)), res.err
}

func isMsiUninstall(uninstallString string) bool {
	return strings.Contains(strings.ToLower(uninstallString), "msiexec")
}

func runMsiUninstall(ctx context.Context, p *installedProgram, args []string) (int, string, error) {
	code, err := msiProductCode(p)
	if err != nil {
		return -1, "", err
	}

	// /x with the ProductCode is the uninstall. /qn and /norestart are
	// unconditional for the same reason they are on install: an MSI uninstall
	// without /qn opens the Windows Installer UI on the endpoint and waits.
	msiArgs := []string{"/x", code, "/qn", "/norestart"}
	msiArgs = append(msiArgs, args...)

	res := runProcess(ctx, "msiexec.exe", msiArgs)
	out := decodeOutput([]byte(res.output))
	if res.err != nil {
		return res.exitCode, out, fmt.Errorf("msiexec /x %s: %w", code, res.err)
	}
	return res.exitCode, out, nil
}

// msiProductCode pulls the GUID out of an msiexec UninstallString.
//
// The registry does not store it: a survey of 76 entries found ProductCode as a
// value in zero of them. It appears only inline in the command, as
// `MsiExec.exe /I{GUID}`, and the uninstall form is the same GUID under /X.
func msiProductCode(p *installedProgram) (string, error) {
	if p.ProductCode != "" {
		return p.ProductCode, nil
	}
	raw := p.UninstallString
	if raw == "" {
		return "", fmt.Errorf("no uninstall command recorded for %s", p.Name)
	}
	// A GUID is 8-4-4-4-12 hex in braces. Scanning for it beats splitting on
	// spaces, which the survey showed is unreliable because these commands are
	// often unquoted and contain spaces.
	start := strings.Index(raw, "{")
	if start < 0 {
		return "", fmt.Errorf("no MSI product code found in %q for %s", raw, p.Name)
	}
	end := strings.Index(raw[start:], "}")
	if end < 0 {
		return "", fmt.Errorf("malformed MSI product code in %q for %s", raw, p.Name)
	}
	code := raw[start : start+end+1]
	if len(code) != 38 { // {8-4-4-4-12} with braces
		return "", fmt.Errorf("implausible MSI product code %q for %s", code, p.Name)
	}
	return code, nil
}

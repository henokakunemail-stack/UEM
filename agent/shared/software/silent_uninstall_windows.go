//go:build windows

package software

import (
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/service"
	"golang.org/x/sys/windows"
)

// tokenElevation mirrors the Windows SDK TOKEN_ELEVATION structure.
type tokenElevation struct {
	TokenIsElevated uint32
}

// isElevated reports whether the current process holds an elevated token.
// An elevated process spawns child processes that inherit elevation without
// triggering a UAC prompt on the interactive desktop.
func isElevated() bool {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()

	var elevation tokenElevation
	var returnedLen uint32
	err = windows.GetTokenInformation(
		token,
		windows.TokenElevation,
		(*byte)(unsafe.Pointer(&elevation)),
		uint32(unsafe.Sizeof(elevation)),
		&returnedLen,
	)
	if err != nil {
		return false
	}
	return elevation.TokenIsElevated != 0
}

// requireElevatedForUninstall refuses to remove anything unless the agent itself
// runs with an administrator token or under the Service Control Manager.
//
// The reason is UAC, and it is the popup an endpoint's user actually sees. An
// uninstaller launched from a normal user session inherits that session's token,
// so the first thing it does when it needs administrator rights is put a consent
// dialog on the screen. Nothing in the agent can suppress that: CREATE_NO_WINDOW
// governs console windows, not the secure-desktop prompt the shell raises on
// elevation.
//
// An agent process running elevated (e.g. as a Windows Service, LocalSystem, or
// elevated Administrator) spawns children with an elevated token and never prompts.
func requireElevatedForUninstall() error {
	if service.RunAsService() || isElevated() {
		return nil
	}
	return fmt.Errorf(
		"%w: an uninstaller needs an administrator token, and one launched from "+
			"this agent's user session would raise a UAC consent dialog on the "+
			"endpoint. Run the agent as Administrator or install as a Windows Service "+
			"(sc.exe create endpoint-agent binPath= \"<agent.exe>\"), then try again", ErrNotElevated)
}

// frameworkSilentSwitches returns well-known unattended switches for known installer
// frameworks when QuietUninstallString is omitted by the vendor.
func frameworkSilentSwitches(p *installedProgram) []string {
	if p == nil {
		return nil
	}
	if p.Framework == "inno" {
		return []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART"}
	}
	if p.Framework == "nsis" {
		return []string{"/S"}
	}

	base := extractUninstallerBase(p.UninstallString)
	if isInnoExeName(base) {
		return []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART"}
	}
	if base == "uninstall.exe" || base == "uninst.exe" {
		return []string{"/S"}
	}
	return nil
}

func extractUninstallerBase(cmd string) string {
	s := strings.TrimSpace(cmd)
	if s == "" {
		return ""
	}
	if s[0] == '"' {
		if end := strings.Index(s[1:], `"`); end >= 0 {
			return strings.ToLower(filepath.Base(s[1 : 1+end]))
		}
	}
	for _, tok := range strings.Fields(s) {
		if strings.HasSuffix(strings.ToLower(tok), ".exe") {
			return strings.ToLower(filepath.Base(tok))
		}
	}
	return strings.ToLower(filepath.Base(s))
}

func isInnoExeName(base string) bool {
	if !strings.HasPrefix(base, "unins") || !strings.HasSuffix(base, ".exe") {
		return false
	}
	digits := base[5 : len(base)-4]
	if len(digits) == 0 {
		return false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return false
		}
	}
	return true
}

// resolveSilentArgs returns the switches that make this program's own
// uninstaller run unattended, or an error saying why none are known.
//
// MSI is silent by construction (/x with ProductCode plus /qn /norestart).
// Vendor quiet commands recorded in the registry are preferred and validated.
// For native uninstallers lacking QuietUninstallString, standard unattended
// switches are safely applied for proven installer frameworks (Inno Setup, NSIS).
func resolveSilentArgs(p *installedProgram) ([]string, error) {
	if isMsiUninstall(p.UninstallString) {
		// No switches needed: runMsiUninstall supplies /x, /qn and /norestart
		// itself. Returning an empty list is correct, and the caller must not
		// treat empty as "refuse" -- that rule belongs to the catalog path, where
		// args come from a person.
		return nil, nil
	}

	quiet := strings.TrimSpace(p.QuietString)
	if quiet == "" {
		if switches := frameworkSilentSwitches(p); len(switches) > 0 {
			return switches, nil
		}
		return nil, fmt.Errorf(
			"%s records no QuietUninstallString, so there is no silent command "+
				"for its uninstaller that can be verified from the endpoint. Running "+
				"it without one opens an interactive window on the endpoint and waits "+
				"for someone who is not there, so nothing was run. Remove it from "+
				"Software -> Removal with explicit uninstall arguments, or uninstall "+
				"it manually", p.display())
	}

	parsed := splitArgs(quiet)

	// A recorded quiet command is a full command line that begins with the
	// uninstaller's own path, and runPlatformUninstall already supplies the
	// executable from UninstallString. Passing that path again as an argument
	// hands the program a filename it did not ask for, which for an installer-
	// framework uninstaller can be read as a target to remove.
	//
	// The leading path is cut from the raw string rather than from the token
	// list, because the registry writes this value in two shapes. A quoted path
	// stays one token, but an UNQUOTED path containing spaces -- a shape the
	// registry survey found 25 times in UninstallString alone -- splits across
	// several tokens, and no single token then equals the whole path. Comparing
	// tokens leaked "C:\Program" and "Files\App\unins000.exe" into argv as if
	// they were arguments.
	//
	// The .exe test is what keeps this from eating a real switch:
	// uninstallerPath falls back to the first token for a value it cannot
	// resolve, so for a bare "/S /NORESTART" it would return "/S" and the strip
	// would eat the only switch there is.
	if path := uninstallerPath(quiet); strings.EqualFold(filepath.Ext(path), ".exe") {
		for _, candidate := range []string{path, `"` + path + `"`} {
			if len(quiet) >= len(candidate) && strings.EqualFold(quiet[:len(candidate)], candidate) {
				parsed = splitArgs(strings.TrimSpace(quiet[len(candidate):]))
				break
			}
		}
	}

	// Whatever survives the strip has to be switches and nothing else. A token
	// that is not one is a leftover fragment of a path uninstallerPath resolved
	// more narrowly than the text spells out -- only "C:\Program" existing, say,
	// while the registry value continues "C:\Program Files\App\unins000.exe".
	// Shipping that fragment hands the uninstaller a filename it did not ask
	// for, and a bare unquoted path with no switches reduces to fragments that a
	// length check cannot catch, so the uninstaller would run with ZERO silent
	// switches -- precisely the interactive uninstall this module exists to
	// prevent. Refusing is the same answer as having no quiet command.
	for _, tok := range parsed {
		if !strings.HasPrefix(tok, "/") && !strings.HasPrefix(tok, "-") {
			return nil, fmt.Errorf(
				"%s records a QuietUninstallString whose arguments could not be "+
					"separated from its own uninstaller path (%q), so no silent "+
					"command could be verified; nothing was run",
				p.display(), quiet)
		}
	}

	// The switches have to belong to the binary that will actually run.
	// runPlatformUninstall launches uninstallerPath(UninstallString), so a quiet
	// command naming a different executable is a set of instructions for some
	// other program. Granting "verified silent" on that basis is a verdict about
	// a program that is never executed: an NSIS uninstaller handed Inno Setup's
	// /VERYSILENT ignores them and raises its own confirmation dialog anyway.
	// Every entry on the surveyed machine paired a quiet command with the same
	// binary, so refusing a mismatch costs nothing real and is the direction the
	// whole module is built around -- refuse what cannot be verified.
	if path := uninstallerPath(quiet); !strings.EqualFold(path, uninstallerPath(p.UninstallString)) {
		return nil, fmt.Errorf(
			"%s records a QuietUninstallString for a different executable (%s) "+
				"than the one its UninstallString runs (%s), so its silent switches "+
				"cannot be verified against the program that would be removed; "+
				"nothing was run",
			p.display(), path, uninstallerPath(p.UninstallString))
	}

	if len(parsed) == 0 {
		// Either the value would not parse, or it named only the uninstaller with
		// no switches at all. Both are the same situation as having no quiet
		// command: nothing here can be verified as silent, and running it opens a
		// window on the endpoint.
		return nil, fmt.Errorf(
			"%s records a QuietUninstallString that carries no usable arguments (%q), "+
				"so no silent command could be verified; nothing was run",
			p.display(), quiet)
	}
	return parsed, nil
}

// isRebootRequiredExit reports that a run which exited non-zero nonetheless
// finished the removal, pending a restart.
//
// It lives here rather than in the build-tag-free caller because the codes it
// knows are msiexec's, and isMsiUninstall only exists on Windows. runProcess
// reports any non-zero exit as an error, so this has to be read off the exit
// code rather than off the error; without it, a 3010 -- the ordinary way an MSI
// says it removed something but could not delete a file in use -- would be
// recorded as a failed removal on a machine where the program is in fact gone.
func isRebootRequiredExit(p *installedProgram, code int) bool {
	return isMsiUninstall(p.UninstallString) && isMSISuccessCode(code)
}

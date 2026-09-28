package software

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UninstallPayload is what the server sends for a software.uninstall command.
//
// It carries no download URL or file name on purpose. The installer is already
// on the endpoint and the package file is not, so the agent has to work out
// from the endpoint itself what to remove. The server's package_type and
// uninstall_args are the operator's declared intent, not a file to trust.
type UninstallPayload struct {
	TaskID        string `json:"task_id"`
	PackageID     string `json:"package_id"`
	PackageName   string `json:"package_name"`
	PackageType   string `json:"package_type"`
	UninstallArgs string `json:"uninstall_args"`
}

// ErrNotInstalled is returned when the package is simply not on the machine.
//
// It is a success, not a failure. Compliance work means asserting that a
// machine does not have a program, and a machine that never had it satisfies
// the requirement. Reporting it as an error would put a red row in front of an
// operator for a machine that is already in the desired state.
var ErrNotInstalled = errors.New("package is not installed on this endpoint")

// Uninstall removes one package, silently.
//
// The contract is narrow and deliberate: either the program is gone when this
// returns, or the caller gets an error saying precisely why it is not. There is
// no "probably worked" outcome, because a compliance report built on a probably
// is worse than no report.
//
// What it refuses to do is the thing that makes uninstall dangerous: it will
// not guess at a target. Uninstalling the wrong application is worse than
// uninstalling nothing, and there is no undo. So when a name matches more than
// one installed program, the ambiguity is reported rather than resolved.
func Uninstall(ctx context.Context, payload UninstallPayload) (exitCode int, output string, err error) {
	packageName := strings.TrimSpace(payload.PackageName)
	if packageName == "" {
		return -1, "", errors.New("uninstall requires a package name to match against")
	}
	// The same guard the install path has: an uninstaller with no switches opens
	// a window on someone's desktop and waits for a human. The server rejects
	// this too, but a package predating that rule can still reach here.
	args := splitArgs(payload.UninstallArgs)
	if len(args) == 0 {
		return -1, "", fmt.Errorf(
			"no silent-uninstall arguments for %s: an uninstall must supply them "+
				"(for an MSI /x with its ProductCode, for a native uninstaller its own "+
				"silent switch such as /s), otherwise it opens an interactive window on the endpoint",
			packageName)
	}

	found, err := findInstalled(ctx, packageName)
	if err != nil {
		return -1, "", err
	}
	if len(found) == 0 {
		return 0, fmt.Sprintf("%s is not installed on this endpoint; nothing to remove", packageName), ErrNotInstalled
	}

	// A name that matches one program exactly has named that program, even when
	// a longer name also starts with it. Without this, "Microsoft Edge" could
	// never be uninstalled on a machine that also has "Microsoft Edge WebView2
	// Runtime" -- the exact match and the prefix match tie, the ambiguity check
	// fires, and the operator is told to narrow a query that is already exact.
	// Real registry data on one workstation: 47 entries, and this is the pair
	// that shows up.
	if exact := exactMatches(found, packageName); len(exact) > 0 {
		found = exact
	}
	if len(found) > 1 {
		names := make([]string, 0, len(found))
		for _, f := range found {
			names = append(names, f.display())
		}
		// Refusing here is the whole safety argument for this feature. Picking one
		// of several matching programs risks removing an application nobody
		// named.
		return -1, "", fmt.Errorf(
			"%q matches %d installed programs (%s): refusing to guess which one to remove. "+
				"Narrow the package name, or remove the others first",
			packageName, len(found), strings.Join(names, ", "))
	}

	target := found[0]
	return runPlatformUninstall(ctx, target, args)
}

// exactMatches returns the candidates whose name normalises to exactly the
// query, ignoring any whose name is longer. It is what makes an exact query
// beat a prefix one.
//
// A query with no exact match is not narrowed by this: the caller keeps the
// full set and reports the ambiguity, because "Python 3.12.10" matching ten
// Python sub-components is a real situation where the operator has to choose,
// not a case where a near-miss decides for them.
func exactMatches(found []*installedProgram, query string) []*installedProgram {
	want := normalizeProgramName(query)
	if want == "" {
		return nil
	}
	var out []*installedProgram
	for _, f := range found {
		if normalizeProgramName(f.Name) == want {
			out = append(out, f)
		}
	}
	return out
}

// installedProgram is one candidate the agent found on this machine.
type installedProgram struct {
	Name        string
	Version     string
	Publisher   string
	ProductCode string
	// UninstallString is the raw command the OS recorded, used only to locate
	// the uninstaller binary. It is never executed as-is: it is a bare,
	// frequently unquoted path, and running it verbatim is how a silent
	// uninstall turns into an interactive one.
	UninstallString string
	QuietString     string
	// PackageID is the platform-native identity (a dpkg name, an rpm label, a
	// pkg receipt id) and is the only thing an OS-native uninstaller is invoked
	// by. Empty on Windows, where the recorded command is the mechanism.
	PackageID string
	// Description carries a one-line summary on platforms that record one.
	Description string
}

func (p *installedProgram) display() string {
	if p.Version != "" {
		return fmt.Sprintf("%s %s", p.Name, p.Version)
	}
	return p.Name
}

// uninstallerPath extracts the executable from a Windows UninstallString.
//
// The quoted form is easy: everything up to the closing quote. The unquoted form
// is not, and it is the common one -- WinRAR records a bare
// `C:\Program Files\WinRAR\Uninstall.exe` with no quoting at all. Splitting on
// the first space there yields `C:\Program`, which is not a file, and the
// uninstall fails on the one package it was built for.
//
// So an unquoted string is scanned for the longest prefix that is an existing
// file. That is the only reading of a genuinely ambiguous string that can be
// verified, and it cannot invent a path: a prefix either exists or it does not.
// The recorded switches are dropped either way, since running them is how a
// silent uninstall turns into an interactive one.
func uninstallerPath(uninstallString string) string {
	s := strings.TrimSpace(uninstallString)
	if s == "" {
		return ""
	}
	if s[0] == '"' {
		if end := strings.Index(s[1:], `"`); end >= 0 {
			return s[1 : 1+end]
		}
		return s[1:]
	}

	// The whole string, when it is a file as recorded.
	if isRegularFile(s) {
		return s
	}
	// Otherwise the longest existing prefix, trying successively shorter
	// cut-points. Checking from the rightmost space backwards finds
	// "C:\Program Files\App\uninstall.exe" before "C:\Program Files\App", which
	// is the ordering that matters: the first is the executable, the second is
	// a directory that happens to exist.
	for i := strings.LastIndexByte(s, ' '); i > 0; i = strings.LastIndexByte(s[:i], ' ') {
		if isRegularFile(s[:i]) {
			return s[:i]
		}
	}
	// Nothing resolved. Return the first token so the caller's error names a
	// concrete path rather than echoing the whole registry string back.
	if sp := strings.IndexByte(s, ' '); sp > 0 {
		return s[:sp]
	}
	return s
}

func isRegularFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// resolveUninstallerPath refuses a path that is not a real file.
//
// A recorded UninstallString is a hint from a program that may since have been
// removed. Running whatever path it names, without checking, is arbitrary code
// execution driven by a registry value.
func resolveUninstallerPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("the installed program records no uninstaller path")
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("uninstaller path %q is not absolute; refusing to run it", p)
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("uninstaller %s is not present on this endpoint: %w", p, err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("uninstaller path %s is a directory", p)
	}
	return p, nil
}

package software

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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

	target, err := resolveTarget(ctx, packageName)
	if err != nil {
		// resolveTarget reports ErrNotInstalled with the operator-facing text
		// already in the message slot, because a caller that treats it as a
		// success still wants that sentence in the result rather than an empty
		// string. It has to be checked INSIDE this branch: a blanket return above
		// it makes the next line unreachable, which is how the catalog path came
		// to report an absent package as exit -1 with a blank output log.
		if errors.Is(err, ErrNotInstalled) {
			return 0, err.Error(), err
		}
		return -1, "", err
	}

	exitCode, output, runErr := runPlatformUninstall(ctx, target, args)
	if runErr != nil && isRebootRequiredExit(target, exitCode) {
		return exitCode, output, nil
	}
	if runErr == nil {
		if verifyErr := verifyUninstalled(ctx, target.Name); verifyErr != nil {
			return exitCode, output, verifyErr
		}
	}
	return exitCode, output, runErr
}

// findInstalledFn is indirected so tests can exercise verification without touching OS registry.
var findInstalledFn = findInstalled

// verifyUninstalled asserts that the target software was actually removed from
// the endpoint. It re-queries findInstalled with bounded polling to give OS
// caches and registry flushes a few seconds to settle. If the program remains
// installed, it returns an error to prevent reporting false success.
func verifyUninstalled(ctx context.Context, name string) error {
	deadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		found, err := findInstalledFn(ctx, name)
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%s remains registered on endpoint after uninstaller completed", name)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// resolveTarget finds the one installed program a name refers to, or refuses.
//
// It is the guard half of Uninstall, extracted so that every caller gets the
// same answer for the same name. That matters because the second caller --
// UninstallByName -- resolves its own switches instead of taking them from the
// server, and a second copy of this logic is exactly how an ambiguity check
// ends up applied on one path and forgotten on the other.
//
// The three refusals here are the safety argument for the whole feature:
//   - no match is not installed, which the callers treat as success, because a
//     machine that never had the program already satisfies a compliance rule
//   - an exact match beats a prefix match, so "Microsoft Edge" still names one
//     program on a box that also has Edge WebView2 Runtime
//   - several matches are reported, never guessed at; there is no undo
func resolveTarget(ctx context.Context, packageName string) (*installedProgram, error) {
	found, err := findInstalledFn(ctx, packageName)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("%s is not installed on this endpoint; nothing to remove: %w",
			packageName, ErrNotInstalled)
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
		return nil, fmt.Errorf(
			"%q matches %d installed programs (%s): refusing to guess which one to remove. "+
				"Narrow the package name, or remove the others first",
			packageName, len(found), strings.Join(names, ", "))
	}

	return found[0], nil
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
	// Framework records an identified installer framework (e.g. "inno", "nsis").
	Framework string
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

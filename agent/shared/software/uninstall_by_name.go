package software

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNotElevated marks a refusal that exists only to keep a dialog off someone's
// screen. It is separate from a failure so a caller can tell "this agent cannot
// remove anything unattended" apart from "the uninstaller ran and failed" -- the
// first needs an operator to change how the agent is installed, the second might
// be worth a retry.
var ErrNotElevated = errors.New("agent is not running as a Windows Service")

// elevationCheck is the elevation gate, indirected only so a test can exercise
// the paths behind it. Production always uses requireElevatedForUninstall,
// which reads the real Service Control Manager; a test that could set this to a
// stub could also ship a stub, so it is unexported and there is no setter.
var elevationCheck = requireElevatedForUninstall

// UninstallByName removes a program named by an operator working from a device's
// installed-software list, and derives the switches itself.
//
// This is the entry point for the console's per-row Uninstall button, and it is
// deliberately NOT the catalog deployment path. That path takes uninstall_args
// from a package record a human verified when they built the package. This one
// takes only a name, because the programs it targets are by definition the ones
// nobody deployed from the catalog.
//
// The whole design turns on one rule: an uninstall that cannot be silent does
// not run. There is no fallback switch, no default framework guess, and no "run
// it and see". An uninstaller invoked without the switches it needs opens a
// window on an endpoint somebody is using, and waits there for a human who is
// not coming. A wrong guess is not a failed task; it is the exact outcome this
// feature exists to prevent.
//
// Coverage is therefore whatever the endpoint itself can vouch for. An MSI
// package is silent unconditionally, because msiexec's own switches are built
// here rather than supplied. A native uninstaller is silent only if the vendor
// recorded a quiet command -- the registry's QuietUninstallString, and the only
// source of silence that is not a guess. On the machine this was written
// against, 26 of 43 uninstall entries were msiexec-driven and 7 more carried a
// QuietUninstallString, so 33 of 43 are removable here and the rest are refused
// by name. The remaining path for those is Software -> Removal, where an operator
// can supply switches for a package they actually deployed.
func UninstallByName(ctx context.Context, name string) (exitCode int, output string, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return -1, "", errors.New("uninstall requires a software name to match against")
	}

	if err := elevationCheck(); err != nil {
		return -1, "", err
	}

	// The same target resolution the catalog path uses, so an ambiguous or a
	// non-matching name is refused identically on both. resolveTarget runs
	// nothing itself.
	target, err := resolveTarget(ctx, name)
	if err != nil {
		if errors.Is(err, ErrNotInstalled) {
			// Compliance work asserts a program is absent, so its absence is the
			// desired state, not a failure to report.
			return 0, err.Error(), ErrNotInstalled
		}
		return -1, "", err
	}

	// Resolved from the endpoint, never from the request. This is the function
	// that makes the feature silent-or-nothing.
	args, err := resolveSilentArgs(target)
	if err != nil {
		return -1, "", err
	}

	code, out, runErr := runPlatformUninstall(ctx, target, args)
	if runErr != nil && isRebootRequiredExit(target, code) {
		// The package is gone either way; failing a finished removal because
		// Windows wants a restart to drop a locked file would be wrong.
		return code, out + fmt.Sprintf(
			"\n[uninstall completed; exit code %d means a reboot is required to finish]", code), nil
	}
	if runErr == nil {
		if verifyErr := verifyUninstalled(ctx, target.Name); verifyErr != nil {
			return code, out, verifyErr
		}
	}
	return code, out, runErr
}

// DescribeUninstall renders an outcome as the text stored in agent_commands.result.
//
// Every branch is operator-readable on purpose, because this is the only record
// the console has of what happened on the endpoint. The HTTP response said 201
// while the command was merely on its way, and the refusal -- when the program
// has no quiet command, or the agent is not elevated -- arrives afterwards. A
// refusal that reached the operator as "error: uninstall failed" would be a
// refusal nobody can act on.
func DescribeUninstall(name string, exitCode int, output string, err error) string {
	switch {
	case errors.Is(err, ErrNotInstalled):
		return fmt.Sprintf("%s: %s", name, output)
	case err == nil:
		if output != "" {
			return fmt.Sprintf("%s: uninstall completed (exit code %d)\n%s", name, exitCode, output)
		}
		return fmt.Sprintf("%s: uninstall completed (exit code %d)", name, exitCode)
	default:
		// The two failures need different words, and only this code can tell them
		// apart. A refusal is one of this module's own answers -- no quiet
		// command, no elevation, an ambiguous name -- and it means nothing ran, so
		// the program is exactly where it was. An MSI that exits 1603 was refused
		// by msiexec, not here: the uninstaller ran, said no, and the operator
		// needs its diagnostics, which runProcess keeps a bounded tail of for
		// exactly this purpose and this row is the only place they can appear.
		//
		// Exit code -1 is the signal for "this code refused", because every
		// refusal returns it and no real run does.
		if exitCode == -1 {
			head := fmt.Sprintf("%s: uninstall refused: %v", name, err)
			if output != "" {
				return head + "\n" + output
			}
			return head
		}
		head := fmt.Sprintf("%s: uninstall failed (exit code %d): %v", name, exitCode, err)
		if output != "" {
			return head + "\n" + output
		}
		return head
	}
}

//go:build !windows

package software

// requireElevatedForUninstall is a no-op off Windows.
//
// UAC is a Windows mechanism, and it is the only thing that makes an otherwise
// silent command raise a dialog on the endpoint: on Linux and macOS the package
// tools this path runs -- dpkg, rpm, pkgutil -- ask for a password on a tty and
// fail immediately when there is none, rather than prompting a user on their
// desktop. A failure there is a clean, diagnosable failure, so adding a refusal
// here would only deny work that was going to work.
func requireElevatedForUninstall() error {
	return nil
}

// isRebootRequiredExit reports that a failed run nonetheless finished the
// removal, pending a restart.
//
// Always false off Windows: the reboot-required exit codes belong to msiexec,
// which is the Windows uninstall mechanism. The equivalent here is a package
// manager exiting non-zero, and treating that as "done, just restart" would
// report a removal that did not happen.
func isRebootRequiredExit(p *installedProgram, code int) bool {
	return false
}

// resolveSilentArgs derives nothing on Linux and macOS.
//
// The Windows problem this exists for does not arise here. On these platforms the
// uninstaller is the operating system's own package manager rather than a
// program's private GUI: runPlatformUninstall already invokes dpkg -P, rpm -e or
// pkgutil --forget, none of which has an interactive mode and none of which the
// program's publisher controls. There is no vendor switch to be missing, so an
// empty result is the correct answer rather than a gap.
func resolveSilentArgs(p *installedProgram) ([]string, error) {
	return nil, nil
}

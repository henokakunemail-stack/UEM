//go:build !windows && !linux && !darwin

package software

import (
	"context"
	"fmt"
	"runtime"
)

// runPlatformUninstall on an operating system this agent has no uninstaller for.
//
// The three supported targets each need their own mechanism: msiexec and the
// recorded uninstall command on Windows, dpkg or rpm on Linux, pkgutil receipts
// on macOS. Guessing a fourth would be running an unknown command against an
// endpoint, so this refuses instead.
func runPlatformUninstall(ctx context.Context, p *installedProgram, args []string) (int, string, error) {
	return -1, "", fmt.Errorf("silent uninstall is not implemented for %s", runtime.GOOS)
}

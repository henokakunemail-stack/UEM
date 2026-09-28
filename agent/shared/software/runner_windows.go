//go:build windows

package software

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

type windowsRunner struct{}

func DefaultRunner() Runner {
	return &windowsRunner{}
}

func (r *windowsRunner) Run(ctx context.Context, filePath, packageType, installArgs string) (int, string, error) {
	var cmd *exec.Cmd

	switch strings.ToLower(packageType) {
	case "msi":
		args := []string{"/i", filePath}
		if installArgs == "" {
			args = append(args, "/qn", "/norestart")
		} else {
			args = append(args, strings.Fields(installArgs)...)
		}
		cmd = exec.CommandContext(ctx, "msiexec.exe", args...)

	case "exe":
		// A bare .exe run with no switches is how a GUI installer escapes to the
		// desktop: the WinRAR SFX, for one, shows a setup dialog that blocks on a
		// human and never returns, so the task sits in 'installing' forever while
		// the operator's machine is being interrupted. The console requires
		// install_args for exe at upload, so an empty value here means the package
		// predates that rule; refuse it rather than open a dialog on someone's
		// desktop. There is no safe universal silent switch -- NSIS wants /S,
		// Inno Setup wants /VERYSILENT, WinRAR's SFX module wants /s -- so the
		// flags have to come from the package.
		if installArgs == "" {
			return -1, "", fmt.Errorf(
				"no silent-install arguments for %s: an .exe package must supply them " +
					"(for example /S, /VERYSILENT /SUPPRESSMSGBOXES /NORESTART, or /s for a WinRAR SFX), " +
					"otherwise it opens an interactive window on the endpoint", filepath.Base(filePath))
		}
		cmd = exec.CommandContext(ctx, filePath, strings.Fields(installArgs)...)

	case "script", "ps1":
		args := []string{"-ExecutionPolicy", "Bypass", "-NoProfile", "-NonInteractive", "-File", filePath}
		if installArgs != "" {
			args = append(args, strings.Fields(installArgs)...)
		}
		cmd = exec.CommandContext(ctx, "powershell.exe", args...)

	case "bat", "cmd":
		args := []string{"/c", filePath}
		if installArgs != "" {
			args = append(args, strings.Fields(installArgs)...)
		}
		cmd = exec.CommandContext(ctx, "cmd.exe", args...)

	default:
		return -1, "", fmt.Errorf("unsupported windows package type: %s", packageType)
	}

	outBytes, err := cmd.CombinedOutput()
	outStr := decodeOutput(outBytes)

	if err == nil {
		return 0, outStr, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus(), outStr, err
		}
		return exitErr.ExitCode(), outStr, err
	}

	return -1, outStr, err
}

//go:build windows

package software

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

type windowsRunner struct{}

func DefaultRunner() Runner {
	return &windowsRunner{}
}

func (r *windowsRunner) Run(ctx context.Context, filePath, packageType, installArgs string) (int, string, error) {
	var (
		name string
		args []string
	)

	switch strings.ToLower(packageType) {
	case "msi":
		// /qn and /norestart are appended unconditionally, and install_args is
		// additive on top of them. They used to be either/or, which turned any
		// operator-entered argument into a way to make a silent install
		// interactive: typing /L*v C:\install.log to get a diagnostic log
		// produced msiexec with no /qn, so the MSI's own UI opened on the
		// endpoint and blocked on a human, and the /norestart protection was
		// gone too. install_args is a log path or a property assignment, never
		// a reason to drop the switches that make the install unattended.
		args = []string{"/i", filePath, "/qn", "/norestart"}
		args = append(args, splitArgs(installArgs)...)
		name = "msiexec.exe"

	case "exe":
		// A bare .exe run with no switches is how a GUI installer escapes to the
		// desktop: the WinRAR SFX, for one, shows a setup dialog that blocks on a
		// human and never returns, so the task sits in 'installing' forever while
		// the operator's machine is being interrupted. The console requires
		// install_args for exe at upload, so an empty value here means the package
		// predates that rule; refuse it rather than open a dialog on someone's
		// desktop. There is no safe universal silent switch -- NSIS wants /S,
		// Inno Setup wants /VERYSILENT /SUPPRESSMSGBOXES /NORESTART, WinRAR's SFX
		// module wants /s -- so the flags have to come with the package. That is
		// the general shape of this whole feature: the agent knows how to run a
		// package silently, not which switch silences a given package, and every
		// new package type an operator uploads has to bring its own flags.
		//
		// Emptiness is tested on the parsed slice, not on the raw string.
		// installArgs of "   " is not empty but splitArgs returns nothing for
		// it, and launching bare is the exact failure this guard exists to
		// stop -- the upload-time check in the server trims whitespace, but a
		// package that predates that rule can still reach here.
		args = splitArgs(installArgs)
		if len(args) == 0 {
			return -1, "", fmt.Errorf(
				"no silent-install arguments for %s: an .exe package must supply them "+
					"(for example /S, /VERYSILENT /SUPPRESSMSGBOXES /NORESTART, or /s for a WinRAR SFX), "+
					"otherwise it opens an interactive window on the endpoint", filepath.Base(filePath))
		}
		name = filePath

	case "script", "ps1":
		name = "powershell.exe"
		args = []string{"-ExecutionPolicy", "Bypass", "-NoProfile", "-NonInteractive", "-File", filePath}
		if installArgs != "" {
			args = append(args, splitArgs(installArgs)...)
		}

	case "bat", "cmd":
		name = "cmd.exe"
		args = []string{"/c", filePath}
		if installArgs != "" {
			args = append(args, splitArgs(installArgs)...)
		}

	default:
		return -1, "", fmt.Errorf("unsupported windows package type: %s", packageType)
	}

	res := runProcess(ctx, name, args)
	return res.exitCode, decodeOutput([]byte(res.output)), res.err
}

//go:build darwin

package software

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

type darwinRunner struct{}

func DefaultRunner() Runner {
	return &darwinRunner{}
}

func (r *darwinRunner) Run(ctx context.Context, filePath, packageType, installArgs string) (int, string, error) {
	var cmd *exec.Cmd

	switch strings.ToLower(packageType) {
	case "pkg":
		args := []string{"-pkg", filePath, "-target", "/"}
		if installArgs != "" {
			args = append(args, strings.Fields(installArgs)...)
		}
		cmd = exec.CommandContext(ctx, "/usr/sbin/installer", args...)

	case "script", "sh":
		args := []string{filePath}
		if installArgs != "" {
			args = append(args, strings.Fields(installArgs)...)
		}
		cmd = exec.CommandContext(ctx, "/bin/sh", args...)

	default:
		return -1, "", fmt.Errorf("unsupported darwin package type: %s", packageType)
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

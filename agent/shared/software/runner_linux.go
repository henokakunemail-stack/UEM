//go:build linux

package software

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

type linuxRunner struct{}

func DefaultRunner() Runner {
	return &linuxRunner{}
}

func (r *linuxRunner) Run(ctx context.Context, filePath, packageType, installArgs string) (int, string, error) {
	var cmd *exec.Cmd

	switch strings.ToLower(packageType) {
	case "deb":
		args := []string{"-i", filePath}
		if installArgs != "" {
			args = append(args, strings.Fields(installArgs)...)
		}
		cmd = exec.CommandContext(ctx, "dpkg", args...)

	case "rpm":
		args := []string{"-Uvh", filePath}
		if installArgs != "" {
			args = append(args, strings.Fields(installArgs)...)
		}
		cmd = exec.CommandContext(ctx, "rpm", args...)

	case "script", "sh":
		args := []string{filePath}
		if installArgs != "" {
			args = append(args, strings.Fields(installArgs)...)
		}
		cmd = exec.CommandContext(ctx, "/bin/sh", args...)

	default:
		return -1, "", fmt.Errorf("unsupported linux package type: %s", packageType)
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

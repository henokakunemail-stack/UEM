//go:build linux

package software

import (
	"context"
	"fmt"
	"strings"
)

type linuxRunner struct{}

func DefaultRunner() Runner {
	return &linuxRunner{}
}

func (r *linuxRunner) Run(ctx context.Context, filePath, packageType, installArgs string) (int, string, error) {
	var (
		name string
		args []string
	)

	switch strings.ToLower(packageType) {
	case "deb":
		name = "dpkg"
		args = []string{"-i", filePath}
		if installArgs != "" {
			args = append(args, splitArgs(installArgs)...)
		}

	case "rpm":
		name = "rpm"
		args = []string{"-Uvh", filePath}
		if installArgs != "" {
			args = append(args, splitArgs(installArgs)...)
		}

	case "script", "sh":
		name = "/bin/sh"
		args = []string{filePath}
		if installArgs != "" {
			args = append(args, splitArgs(installArgs)...)
		}

	default:
		return -1, "", fmt.Errorf("unsupported linux package type: %s", packageType)
	}

	res := runProcess(ctx, name, args)
	return res.exitCode, decodeOutput([]byte(res.output)), res.err
}

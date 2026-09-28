//go:build darwin

package software

import (
	"context"
	"fmt"
	"strings"
)

type darwinRunner struct{}

func DefaultRunner() Runner {
	return &darwinRunner{}
}

func (r *darwinRunner) Run(ctx context.Context, filePath, packageType, installArgs string) (int, string, error) {
	var (
		name string
		args []string
	)

	switch strings.ToLower(packageType) {
	case "pkg":
		name = "/usr/sbin/installer"
		args = []string{"-pkg", filePath, "-target", "/"}
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
		return -1, "", fmt.Errorf("unsupported darwin package type: %s", packageType)
	}

	res := runProcess(ctx, name, args)
	return res.exitCode, decodeOutput([]byte(res.output)), res.err
}

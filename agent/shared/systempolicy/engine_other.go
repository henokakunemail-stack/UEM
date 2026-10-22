//go:build !windows

package systempolicy

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

type nonWinBackend struct{}

func newPlatformBackend() platformBackend {
	return &nonWinBackend{}
}

// Apply runs the Linux/macOS decisions (unix_policy.go) against the real
// machine.
func (n *nonWinBackend) Apply(ctx context.Context, _ *http.Client, _ string, s PolicySettings) ([]protocol.SettingResult, error) {
	return unixResults(ctx, s, runtime.GOOS, realUnixSys{}), nil
}

// realUnixSys is unixSys on the actual machine.
type realUnixSys struct{}

func (realUnixSys) WriteFile(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }
func (realUnixSys) Remove(path string) error                 { return os.Remove(path) }

func (realUnixSys) Run(ctx context.Context, name string, args ...string) error {
	if out, err := exec.CommandContext(ctx, name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (realUnixSys) ProvisionAdmin(ctx context.Context, s PolicySettings) error {
	return applyAdminAccountNonWindows(ctx, s)
}

func applyAdminAccountNonWindows(ctx context.Context, s PolicySettings) error {
	username := strings.TrimSpace(s.AdminAccountUsername)
	for _, r := range username {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return fmt.Errorf("invalid username: '%s'", username)
		}
	}

	fullName := s.AdminAccountFullName
	if fullName == "" {
		fullName = "Managed Administrator"
	}

	if runtime.GOOS == "darwin" {
		// macOS sysadminctl
		if err := exec.CommandContext(ctx, "id", username).Run(); err != nil {
			// User does not exist, create admin user
			cmd := exec.CommandContext(ctx, "sysadminctl", "-addUser", username, "-fullName", fullName, "-password", s.AdminAccountPassword, "-admin")
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("sysadminctl addUser: %w (%s)", err, strings.TrimSpace(string(out)))
			}
		} else {
			// User exists, reset password and ensure in admin group
			cmd := exec.CommandContext(ctx, "sysadminctl", "-resetPasswordFor", username, "-newPassword", s.AdminAccountPassword)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("sysadminctl resetPassword: %w (%s)", err, strings.TrimSpace(string(out)))
			}
			_ = exec.CommandContext(ctx, "dseditgroup", "-o", "edit", "-a", username, "-t", "user", "admin").Run()
		}
		return nil
	}

	// Linux provisioning
	userExists := exec.CommandContext(ctx, "getent", "passwd", username).Run() == nil
	if !userExists {
		addCmd := exec.CommandContext(ctx, "useradd", "-m", "-s", "/bin/bash", "-c", fullName, username)
		if out, err := addCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("useradd '%s': %w (%s)", username, err, strings.TrimSpace(string(out)))
		}
	}

	// Set password securely via stdin using chpasswd
	chpasswdCmd := exec.CommandContext(ctx, "chpasswd")
	chpasswdCmd.Stdin = bytes.NewReader([]byte(fmt.Sprintf("%s:%s\n", username, s.AdminAccountPassword)))
	if out, err := chpasswdCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chpasswd '%s': %w (%s)", username, err, strings.TrimSpace(string(out)))
	}

	// Add to admin group: check for sudo first, then wheel
	if exec.CommandContext(ctx, "getent", "group", "sudo").Run() == nil {
		_ = exec.CommandContext(ctx, "usermod", "-aG", "sudo", username).Run()
	} else if exec.CommandContext(ctx, "getent", "group", "wheel").Run() == nil {
		_ = exec.CommandContext(ctx, "usermod", "-aG", "wheel", username).Run()
	}

	if s.AdminAccountNeverExpires {
		_ = exec.CommandContext(ctx, "chage", "-M", "99999", username).Run()
	}

	return nil
}

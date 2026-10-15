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
	"strconv"
	"strings"
)

type nonWinBackend struct{}

func newPlatformBackend() platformBackend {
	return &nonWinBackend{}
}

func (n *nonWinBackend) Apply(ctx context.Context, client *http.Client, serverURL string, s PolicySettings) (int, string, error) {
	applied := 0
	var degraded []string

	// Linux USB Storage Restriction
	if s.USBStorageMode == USBStorageBlock {
		// Blacklist usb-storage and uas kernel modules to block flash drives and external disks
		_ = os.WriteFile("/etc/modprobe.d/endpoint-block-usb.conf", []byte("blacklist usb_storage\nblacklist uas\n"), 0644)
		_ = exec.CommandContext(ctx, "modprobe", "-r", "uas").Run()
		_ = exec.CommandContext(ctx, "modprobe", "-r", "usb_storage").Run()
		applied++
	} else if s.USBStorageMode == USBStorageAllow {
		_ = os.Remove("/etc/modprobe.d/endpoint-block-usb.conf")
	}

	// Linux / GNOME desktop background (if gsettings available)
	if s.WallpaperEnabled && s.WallpaperURL != "" {
		_ = exec.CommandContext(ctx, "gsettings", "set", "org.gnome.desktop.background", "picture-uri", s.WallpaperURL).Run()
		applied++
	}

	// Linux / GNOME inactivity lock (if gsettings available)
	if s.InactivityTimeoutMinutes > 0 {
		idleSecs := strconv.Itoa(s.InactivityTimeoutMinutes * 60)
		_ = exec.CommandContext(ctx, "gsettings", "set", "org.gnome.desktop.session", "idle-delay", idleSecs).Run()
		_ = exec.CommandContext(ctx, "gsettings", "set", "org.gnome.desktop.screensaver", "lock-enabled", "true").Run()
		applied++
	}

	// Managed Local Administrator Account (Linux & macOS)
	if s.AdminAccountEnabled && s.AdminAccountUsername != "" && s.AdminAccountPassword != "" {
		if err := applyAdminAccountNonWindows(ctx, s); err != nil {
			degraded = append(degraded, "admin_account: "+err.Error())
		} else {
			applied++
		}
	}

	degradedMsg := ""
	if len(degraded) > 0 {
		degradedMsg = strings.Join(degraded, "; ")
	}

	return applied, degradedMsg, nil
}

func applyAdminAccountNonWindows(ctx context.Context, s PolicySettings) error {
	username := strings.TrimSpace(s.AdminAccountUsername)
	for _, r := range username {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
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


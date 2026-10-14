//go:build !windows

package systempolicy

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strconv"
)

type nonWinBackend struct{}

func newPlatformBackend() platformBackend {
	return &nonWinBackend{}
}

func (n *nonWinBackend) Apply(ctx context.Context, client *http.Client, serverURL string, s PolicySettings) (int, string, error) {
	applied := 0

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

	return applied, "", nil
}

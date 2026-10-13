//go:build !windows

package systempolicy

import (
	"context"
	"net/http"
	"os/exec"
	"strconv"
)

type nonWinBackend struct{}

func newPlatformBackend() platformBackend {
	return &nonWinBackend{}
}

func (n *nonWinBackend) Apply(ctx context.Context, client *http.Client, serverURL string, s PolicySettings) (int, string, error) {
	applied := 0

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

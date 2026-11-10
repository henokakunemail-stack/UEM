//go:build !windows

package systempolicy

import "errors"

// WallpaperRefreshFlag mirrors the Windows flag so the agent's flag set is the
// same on every platform.
const WallpaperRefreshFlag = "wallpaper-refresh"

// ReapplyWallpaperAtLogon is Windows-only: per-user policy lives in each
// user's registry hive there.
func ReapplyWallpaperAtLogon(uint32) {}

// ReapplyWallpaperAtStartup is Windows-only.
func ReapplyWallpaperAtStartup() {}

// RunWallpaperRefresh is Windows-only.
func RunWallpaperRefresh(string) error {
	return errors.New("wallpaper refresh is only supported on Windows")
}

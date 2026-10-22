package systempolicy

import (
	"context"
	"errors"
	"io/fs"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

// This file has no build tag on purpose: the Linux and macOS decisions are
// tested on every OS, with a fake unixSys. The backend in engine_other.go
// passes the real one.

// unixSys is what the Linux/macOS backend does to the machine.
type unixSys interface {
	WriteFile(path string, data []byte) error
	Remove(path string) error
	Run(ctx context.Context, name string, args ...string) error
	ProvisionAdmin(ctx context.Context, s PolicySettings) error
}

const usbBlockConf = "/etc/modprobe.d/endpoint-block-usb.conf"

const (
	reasonWindowsOnly = "Windows-only setting; nothing on this OS enforces it"
	// The agent runs as a system service. gsettings run from there writes the
	// service account's own settings, never the logged-in user's desktop, so
	// the wallpaper and lock calls used to "succeed" and change nothing.
	reasonNeedsUserSession = "needs the user's desktop session; the agent runs as a system service and cannot set it"
	reasonMacUSB           = "USB storage control is not implemented on macOS"
	reasonLinuxUSBReadOnly = "read-only USB storage is not implemented on Linux; use block or allow"
)

// unixResults applies what Linux or macOS can enforce and accounts for every
// setting key. A setting left at its default needs nothing and is applied; one
// set to something this OS cannot do is reported unsupported, not skipped.
func unixResults(ctx context.Context, s PolicySettings, goos string, sys unixSys) []protocol.SettingResult {
	var out []protocol.SettingResult

	out = append(out, unixUSB(ctx, s.USBStorageMode, goos, sys))

	if s.WallpaperEnabled {
		out = append(out, unsupported(protocol.SettingWallpaperEnabled, reasonNeedsUserSession))
	} else {
		out = append(out, applied(protocol.SettingWallpaperEnabled))
	}
	if s.InactivityTimeoutMinutes > 0 {
		out = append(out, unsupported(protocol.SettingInactivityTimeoutMinutes, reasonNeedsUserSession))
	} else {
		out = append(out, applied(protocol.SettingInactivityTimeoutMinutes))
	}

	windowsOnly := []struct {
		key string
		set bool
	}{
		{protocol.SettingDisableTaskMgr, s.DisableTaskMgr},
		{protocol.SettingDisableCMD, s.DisableCMD},
		{protocol.SettingDisableRegistryTools, s.DisableRegistryTools},
		{protocol.SettingDisableControlPanel, s.DisableControlPanel},
		{protocol.SettingWindowsUpdateMode, s.WindowsUpdateMode != "" && s.WindowsUpdateMode != WindowsUpdateDefault},
		{protocol.SettingFirewallEnabled, s.FirewallEnabled},
		{protocol.SettingDefenderProtectionEnabled, s.DefenderProtectionEnabled},
		{protocol.SettingDisablePowerShell, s.DisablePowerShell},
		{protocol.SettingDisableAutoplay, s.DisableAutoplay},
		{protocol.SettingScreenSaverTimeoutMinutes, s.ScreenSaverTimeoutMinutes > 0},
		{protocol.SettingScreenSaverPasswordRequired, s.ScreenSaverPasswordRequired},
	}
	for _, w := range windowsOnly {
		if w.set {
			out = append(out, unsupported(w.key, reasonWindowsOnly))
		} else {
			out = append(out, applied(w.key))
		}
	}

	if s.AdminAccountEnabled && s.AdminAccountUsername != "" && s.AdminAccountPassword != "" {
		out = append(out, outcome(protocol.SettingAdminAccountEnabled, sys.ProvisionAdmin(ctx, s)))
	} else {
		out = append(out, applied(protocol.SettingAdminAccountEnabled))
	}
	return out
}

// unixUSB blocks USB storage on Linux by blacklisting its kernel modules. The
// write errors used to be discarded and the block counted as applied -- on
// macOS too, which has no modprobe at all.
func unixUSB(ctx context.Context, mode USBStorageMode, goos string, sys unixSys) protocol.SettingResult {
	key := protocol.SettingUSBStorageMode
	if goos == "darwin" {
		if mode == USBStorageBlock || mode == USBStorageReadOnly {
			return unsupported(key, reasonMacUSB)
		}
		return applied(key)
	}
	switch mode {
	case USBStorageBlock:
		if err := sys.WriteFile(usbBlockConf, []byte("blacklist usb_storage\nblacklist uas\n")); err != nil {
			return failed(key, err)
		}
		// The blacklist stops the modules loading again. A drive in use keeps
		// them loaded until it is removed, so the block is in force but not yet
		// for that drive; say so rather than fail an apply that did its job.
		var unload []error
		for _, mod := range []string{"uas", "usb_storage"} {
			if err := sys.Run(ctx, "modprobe", "-r", mod); err != nil {
				unload = append(unload, err)
			}
		}
		r := applied(key)
		if len(unload) > 0 {
			r.Reason = "blocked for new connections; storage connected now stays usable until removed (" + errors.Join(unload...).Error() + ")"
		}
		return r
	case USBStorageReadOnly:
		return unsupported(key, reasonLinuxUSBReadOnly)
	default:
		if err := sys.Remove(usbBlockConf); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return failed(key, err)
		}
		return applied(key)
	}
}

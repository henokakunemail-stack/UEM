//go:build windows

package systempolicy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type winBackend struct{}

func newPlatformBackend() platformBackend {
	return &winBackend{}
}

func (w *winBackend) Apply(ctx context.Context, client *http.Client, serverURL string, s PolicySettings) (int, string, error) {
	applied := 0
	var degraded []string

	// 1. USB Storage Restriction
	// Strictly restricts Mass Storage Devices while leaving HID mice and keyboards fully functional
	if err := applyUSBStoragePolicy(s.USBStorageMode); err != nil {
		degraded = append(degraded, "usb: "+err.Error())
	} else {
		applied++
	}

	// 2. Wallpaper Policy
	if s.WallpaperEnabled && s.WallpaperURL != "" {
		if err := applyWallpaper(client, serverURL, s.WallpaperURL, s.WallpaperStyle); err != nil {
			degraded = append(degraded, "wallpaper: "+err.Error())
		} else {
			applied++
		}
	} else {
		revertWallpaper()
	}

	// 3. Inactivity Screen Lock
	if err := applyInactivityLock(s.InactivityTimeoutMinutes); err != nil {
		degraded = append(degraded, "inactivity: "+err.Error())
	} else {
		applied++
	}

	// 4. Administrative Restrictions (GPO)
	if err := applyRestrictions(s); err != nil {
		degraded = append(degraded, "restrictions: "+err.Error())
	} else {
		applied++
	}

	// 5. Windows Update
	if err := applyWindowsUpdate(s.WindowsUpdateMode); err != nil {
		degraded = append(degraded, "update: "+err.Error())
	} else {
		applied++
	}

	degradedMsg := ""
	if len(degraded) > 0 {
		degradedMsg = strings.Join(degraded, "; ")
	}

	return applied, degradedMsg, nil
}

// applyUSBStoragePolicy configures USB mass storage driver and Removable Storage Group Policy.
// Mouse and keyboard use hidusb.sys, kbdclass.sys, mouclass.sys and remain 100% active.
func applyUSBStoragePolicy(mode USBStorageMode) error {
	// A. Service driver Start: 3=Demand/Enabled, 4=Disabled
	usbstorKey, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\USBSTOR`, registry.SET_VALUE)
	if err == nil {
		defer usbstorKey.Close()
		if mode == USBStorageBlock {
			_ = usbstorKey.SetDWordValue("Start", 4) // Disabled
		} else {
			_ = usbstorKey.SetDWordValue("Start", 3) // Enabled
		}
	}

	// B. RemovableStorageDevices policy for Disk Drives GUID {53f5630d-b6bf-11d0-94f2-00a0c91efb8b}
	removableKey, _, err := registry.CreateKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Policies\Microsoft\Windows\RemovableStorageDevices\{53f5630d-b6bf-11d0-94f2-00a0c91efb8b}`,
		registry.SET_VALUE,
	)
	if err == nil {
		defer removableKey.Close()
		switch mode {
		case USBStorageBlock:
			_ = removableKey.SetDWordValue("Deny_Read", 1)
			_ = removableKey.SetDWordValue("Deny_Write", 1)
			_ = removableKey.SetDWordValue("Deny_Execute", 1)
		case USBStorageReadOnly:
			_ = removableKey.SetDWordValue("Deny_Read", 0)
			_ = removableKey.SetDWordValue("Deny_Write", 1)
			_ = removableKey.SetDWordValue("Deny_Execute", 0)
		default: // USBStorageAllow
			_ = removableKey.DeleteValue("Deny_Read")
			_ = removableKey.DeleteValue("Deny_Write")
			_ = removableKey.DeleteValue("Deny_Execute")
		}
	}

	// C. StorageDevicePolicies WriteProtect (for USB read-only mode)
	storageKey, _, err := registry.CreateKey(
		registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\StorageDevicePolicies`,
		registry.SET_VALUE,
	)
	if err == nil {
		defer storageKey.Close()
		if mode == USBStorageReadOnly {
			_ = storageKey.SetDWordValue("WriteProtect", 1)
		} else {
			_ = storageKey.SetDWordValue("WriteProtect", 0)
		}
	}

	return nil
}

func applyWallpaper(client *http.Client, serverURL, wallpaperURL string, style WallpaperStyle) error {
	dir := `C:\ProgramData\EndpointManager`
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create wallpaper dir: %w", err)
	}

	destPath := filepath.Join(dir, "corporate-wallpaper.jpg")

	// Resolve full URL if relative
	fullURL := wallpaperURL
	if strings.HasPrefix(fullURL, "/") {
		fullURL = serverURL + fullURL
	}

	// Download or copy file
	if strings.HasPrefix(fullURL, "http://") || strings.HasPrefix(fullURL, "https://") {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fullURL, nil)
		if err != nil {
			return fmt.Errorf("build wallpaper req: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("download wallpaper: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("download wallpaper status: %d", resp.StatusCode)
		}

		out, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("create wallpaper file: %w", err)
		}
		_, err = io.Copy(out, resp.Body)
		out.Close()
		if err != nil {
			return fmt.Errorf("write wallpaper file: %w", err)
		}
	} else if _, err := os.Stat(wallpaperURL); err == nil {
		// Local file path
		destPath = wallpaperURL
	}

	// Style code:
	// Fill: 10, Fit: 6, Stretch: 2, Center: 0, Tile: 1 (with TileWallpaper="1")
	styleCode := "10"
	tileCode := "0"
	switch style {
	case WallpaperFit:
		styleCode = "6"
	case WallpaperStretch:
		styleCode = "2"
	case WallpaperCenter:
		styleCode = "0"
	case WallpaperTile:
		styleCode = "1"
		tileCode = "1"
	default:
		styleCode = "10" // Fill
	}

	// Set GPO Group Policy registry key in Policies\System
	sysPolKey, _, err := registry.CreateKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`,
		registry.SET_VALUE,
	)
	if err == nil {
		defer sysPolKey.Close()
		_ = sysPolKey.SetStringValue("Wallpaper", destPath)
		_ = sysPolKey.SetStringValue("WallpaperStyle", styleCode)
	}

	// Set Default user desktop for new logins
	defDesktopKey, _, err := registry.CreateKey(
		registry.USERS,
		`.DEFAULT\Control Panel\Desktop`,
		registry.SET_VALUE,
	)
	if err == nil {
		defer defDesktopKey.Close()
		_ = defDesktopKey.SetStringValue("Wallpaper", destPath)
		_ = defDesktopKey.SetStringValue("WallpaperStyle", styleCode)
		_ = defDesktopKey.SetStringValue("TileWallpaper", tileCode)
	}

	// Live refresh interactive desktop wallpaper via Win32 API
	user32 := windows.NewLazySystemDLL("user32.dll")
	procSystemParametersInfoW := user32.NewProc("SystemParametersInfoW")
	targetUTF16, err := windows.UTF16PtrFromString(destPath)
	if err == nil {
		// SPI_SETDESKWALLPAPER = 0x0014, SPIF_UPDATEINIFILE = 0x01, SPIF_SENDCHANGE = 0x02
		_, _, _ = procSystemParametersInfoW.Call(
			0x0014,
			0,
			uintptr(unsafe.Pointer(targetUTF16)),
			0x01|0x02,
		)
	}

	// Broadcast parameter change to running explorer sessions
	_ = exec.Command("rundll32.exe", "user32.dll,UpdatePerUserSystemParameters", "1,", "True").Run()

	return nil
}

func revertWallpaper() {
	sysPolKey, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`,
		registry.SET_VALUE,
	)
	if err == nil {
		defer sysPolKey.Close()
		_ = sysPolKey.DeleteValue("Wallpaper")
		_ = sysPolKey.DeleteValue("WallpaperStyle")
	}
}

func applyInactivityLock(minutes int) error {
	k, _, err := registry.CreateKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`,
		registry.SET_VALUE,
	)
	if err != nil {
		return err
	}
	defer k.Close()

	if minutes > 0 {
		secs := uint32(minutes * 60)
		return k.SetDWordValue("InactivityTimeoutSecs", secs)
	}

	_ = k.DeleteValue("InactivityTimeoutSecs")
	return nil
}

func applyRestrictions(s PolicySettings) error {
	// 1. Task Manager & Registry Editor
	kSys, _, err := registry.CreateKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`,
		registry.SET_VALUE,
	)
	if err == nil {
		defer kSys.Close()
		setOrDeleteDWord(kSys, "DisableTaskMgr", s.DisableTaskMgr)
		setOrDeleteDWord(kSys, "DisableRegistryTools", s.DisableRegistryTools)
	}

	// 2. Command Prompt & PowerShell
	kCmd, _, err := registry.CreateKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Policies\Microsoft\Windows\System`,
		registry.SET_VALUE,
	)
	if err == nil {
		defer kCmd.Close()
		setOrDeleteDWord(kCmd, "DisableCMD", s.DisableCMD)
	}

	// 3. Control Panel & PC Settings
	kExp, _, err := registry.CreateKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\Explorer`,
		registry.SET_VALUE,
	)
	if err == nil {
		defer kExp.Close()
		setOrDeleteDWord(kExp, "NoControlPanel", s.DisableControlPanel)
	}

	return nil
}

func setOrDeleteDWord(k registry.Key, name string, enable bool) {
	if enable {
		_ = k.SetDWordValue(name, 1)
	} else {
		_ = k.DeleteValue(name)
	}
}

func applyWindowsUpdate(mode WindowsUpdateMode) error {
	k, _, err := registry.CreateKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU`,
		registry.SET_VALUE,
	)
	if err != nil {
		return err
	}
	defer k.Close()

	switch mode {
	case WindowsUpdateAutoInstall:
		_ = k.SetDWordValue("NoAutoUpdate", 0)
		_ = k.SetDWordValue("AUOptions", 4) // Auto download and schedule install
	case WindowsUpdateNotifyOnly:
		_ = k.SetDWordValue("NoAutoUpdate", 0)
		_ = k.SetDWordValue("AUOptions", 2) // Notify before download
	default:
		_ = k.DeleteValue("NoAutoUpdate")
		_ = k.DeleteValue("AUOptions")
	}

	return nil
}

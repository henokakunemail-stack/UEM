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
		if err := applyWallpaper(client, serverURL, s.WallpaperURL, s.WallpaperStyle, s.WallpaperLocked); err != nil {
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

	// 6. Windows Firewall Enforcement
	if err := applyFirewall(s.FirewallEnabled); err != nil {
		degraded = append(degraded, "firewall: "+err.Error())
	} else if s.FirewallEnabled {
		applied++
	}

	// 7. Windows Defender Real-time Protection
	if err := applyDefenderProtection(s.DefenderProtectionEnabled); err != nil {
		degraded = append(degraded, "defender: "+err.Error())
	} else if s.DefenderProtectionEnabled {
		applied++
	}

	// 8. PowerShell Script Restriction
	if err := applyPowerShellPolicy(s.DisablePowerShell); err != nil {
		degraded = append(degraded, "powershell: "+err.Error())
	} else if s.DisablePowerShell {
		applied++
	}

	// 9. Autoplay / Autorun Restriction
	if err := applyAutoplayPolicy(s.DisableAutoplay); err != nil {
		degraded = append(degraded, "autoplay: "+err.Error())
	} else if s.DisableAutoplay {
		applied++
	}

	// 10. Screen Saver Security Policy
	if err := applyScreenSaverPolicy(s.ScreenSaverTimeoutMinutes, s.ScreenSaverPasswordRequired); err != nil {
		degraded = append(degraded, "screensaver: "+err.Error())
	} else if s.ScreenSaverTimeoutMinutes > 0 || s.ScreenSaverPasswordRequired {
		applied++
	}

	// 11. Managed Local Administrator Account
	if s.AdminAccountEnabled && s.AdminAccountUsername != "" && s.AdminAccountPassword != "" {
		if err := applyAdminAccountPolicy(s); err != nil {
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

// applyUSBStoragePolicy configures USB mass storage driver and Removable Storage Group Policy.
// Mouse and keyboard use hidusb.sys, kbdclass.sys, mouclass.sys and remain 100% active.
func applyUSBStoragePolicy(mode USBStorageMode) error {
	var errs []string

	// A. Service driver Start: 3=Demand/Enabled, 4=Disabled
	usbstorKey, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\USBSTOR`, registry.SET_VALUE)
	if err != nil {
		errs = append(errs, "open USBSTOR key: "+err.Error())
	} else {
		defer usbstorKey.Close()
		if mode == USBStorageBlock {
			if err := usbstorKey.SetDWordValue("Start", 4); err != nil {
				errs = append(errs, "set USBSTOR Start=4: "+err.Error())
			}
		} else {
			if err := usbstorKey.SetDWordValue("Start", 3); err != nil {
				errs = append(errs, "set USBSTOR Start=3: "+err.Error())
			}
		}
	}

	// B. RemovableStorageDevices policy for Disk Drives GUID {53f5630d-b6bf-11d0-94f2-00a0c91efb8b}
	removableKey, _, err := registry.CreateKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Policies\Microsoft\Windows\RemovableStorageDevices\{53f5630d-b6bf-11d0-94f2-00a0c91efb8b}`,
		registry.SET_VALUE,
	)
	if err != nil {
		errs = append(errs, "create RemovableStorageDevices key: "+err.Error())
	} else {
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

	// D. Force Group Policy refresh so RemovableStorageDevices takes effect immediately
	// (without this, policy only applies on next GP cycle ~90 min or reboot)
	_ = exec.Command("gpupdate", "/force", "/wait:0").Run()

	// E. Eject currently mounted USB storage drives so block is enforced instantly
	if mode == USBStorageBlock {
		ejectMountedUSBStorage()
	}

	if len(errs) > 0 {
		return fmt.Errorf("usb policy partial failure: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ejectMountedUSBStorage dismounts all removable USB storage volumes.
// Uses mountvol to enumerate volumes, then unmounts those on removable drives.
func ejectMountedUSBStorage() {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	getDriveType := kernel32.NewProc("GetDriveTypeW")

	for _, letter := range "DEFGHIJKLMNOPQRSTUVWXYZ" {
		root := string(letter) + `:\`
		rootUTF16, err := windows.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		driveType, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(rootUTF16)))
		// DRIVE_REMOVABLE = 2
		if driveType != 2 {
			continue
		}
		// Dismount the volume and lock it
		volPath := `\\.\` + string(letter) + ":"
		pathUTF16, err := windows.UTF16PtrFromString(volPath)
		if err != nil {
			continue
		}
		h, err := windows.CreateFile(pathUTF16,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			continue
		}
		// FSCTL_LOCK_VOLUME = 0x00090018, FSCTL_DISMOUNT_VOLUME = 0x00090020
		var bytesReturned uint32
		_ = windows.DeviceIoControl(h, 0x00090018, nil, 0, nil, 0, &bytesReturned, nil)
		_ = windows.DeviceIoControl(h, 0x00090020, nil, 0, nil, 0, &bytesReturned, nil)
		windows.CloseHandle(h)
	}
}

func applyWallpaper(client *http.Client, serverURL, wallpaperURL string, style WallpaperStyle, locked bool) error {
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
	var styleCode string
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

	// Lock or unlock wallpaper changes via GPO
	for _, keyPath := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\ActiveDesktop`,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`,
	} {
		if k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, keyPath, registry.SET_VALUE); err == nil {
			if locked {
				_ = k.SetDWordValue("NoChangingWallPaper", 1)
				_ = k.SetDWordValue("NoChangingWallpaper", 1)
			} else {
				_ = k.DeleteValue("NoChangingWallPaper")
				_ = k.DeleteValue("NoChangingWallpaper")
			}
			k.Close()
		}
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

	for _, keyPath := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\ActiveDesktop`,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`,
	} {
		if k, err := registry.OpenKey(registry.LOCAL_MACHINE, keyPath, registry.SET_VALUE); err == nil {
			_ = k.DeleteValue("NoChangingWallPaper")
			_ = k.DeleteValue("NoChangingWallpaper")
			k.Close()
		}
	}

	defKey, err := registry.OpenKey(
		registry.USERS,
		`.DEFAULT\Control Panel\Desktop`,
		registry.SET_VALUE,
	)
	if err == nil {
		defer defKey.Close()
		_ = defKey.DeleteValue("Wallpaper")
		_ = defKey.DeleteValue("WallpaperStyle")
		_ = defKey.DeleteValue("TileWallpaper")
	}

	// Broadcast refresh to reset desktop
	_ = exec.Command("rundll32.exe", "user32.dll,UpdatePerUserSystemParameters", "1,", "True").Run()
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
	var errs []string

	// 1. Task Manager & Registry Editor
	kSys, _, err := registry.CreateKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`,
		registry.SET_VALUE,
	)
	if err != nil {
		errs = append(errs, "policies/system: "+err.Error())
	} else {
		defer kSys.Close()
		setOrDeleteDWord(kSys, "DisableTaskMgr", s.DisableTaskMgr, 1)
		setOrDeleteDWord(kSys, "DisableRegistryTools", s.DisableRegistryTools, 1)
	}

	// 2. Command Prompt (value 2 = disable interactive CMD, allow batch files so agent scripts continue)
	kCmd, _, err := registry.CreateKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Policies\Microsoft\Windows\System`,
		registry.SET_VALUE,
	)
	if err != nil {
		errs = append(errs, "policies/system cmd: "+err.Error())
	} else {
		defer kCmd.Close()
		setOrDeleteDWord(kCmd, "DisableCMD", s.DisableCMD, 2)
	}

	// 3. Control Panel & PC Settings (set both keys for classic and modern Windows 10/11 coverage)
	for _, p := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\Explorer`,
		`SOFTWARE\Policies\Microsoft\Windows\Explorer`,
	} {
		kExp, _, err := registry.CreateKey(registry.LOCAL_MACHINE, p, registry.SET_VALUE)
		if err == nil {
			defer kExp.Close()
			setOrDeleteDWord(kExp, "NoControlPanel", s.DisableControlPanel, 1)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("restrictions: %s", strings.Join(errs, "; "))
	}
	return nil
}

func setOrDeleteDWord(k registry.Key, name string, enable bool, val uint32) {
	if enable {
		_ = k.SetDWordValue(name, val)
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

// applyFirewall enforces Windows Defender Firewall across all network profiles.
//
// This and the three policy functions below used to discard every registry and
// netsh error and return nil, so a write refused for lack of rights reached the
// console as an applied policy. Failures are now returned; Apply already
// reports them as degraded.
func applyFirewall(enabled bool) error {
	var errs regErrs
	for _, profile := range []string{"StandardProfile", "DomainProfile", "PublicProfile"} {
		keyPath := fmt.Sprintf(`SOFTWARE\Policies\Microsoft\WindowsFirewall\%s`, profile)
		k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, keyPath, registry.SET_VALUE)
		if err != nil {
			errs.add("open "+profile, err)
			continue
		}
		if enabled {
			errs.add(profile+" EnableFirewall", k.SetDWordValue("EnableFirewall", 1))
		} else {
			errs.add(profile+" EnableFirewall", k.DeleteValue("EnableFirewall"))
		}
		k.Close()
	}
	if enabled {
		if out, err := exec.Command("netsh", "advfirewall", "set", "allprofiles", "state", "on").CombinedOutput(); err != nil {
			errs.add("netsh advfirewall", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out))))
		}
	}
	return errs.err()
}

// applyDefenderProtection prevents disabling Windows Defender real-time antivirus protection
func applyDefenderProtection(enabled bool) error {
	var errs regErrs
	if kDef, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\Policies\Microsoft\Windows Defender`, registry.SET_VALUE); err != nil {
		errs.add("open Windows Defender policy", err)
	} else {
		if enabled {
			errs.add("DisableAntiSpyware", kDef.SetDWordValue("DisableAntiSpyware", 0))
		} else {
			errs.add("DisableAntiSpyware", kDef.DeleteValue("DisableAntiSpyware"))
		}
		kDef.Close()
	}

	if kReal, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\Policies\Microsoft\Windows Defender\Real-Time Protection`, registry.SET_VALUE); err != nil {
		errs.add("open Real-Time Protection policy", err)
	} else {
		for _, name := range []string{"DisableRealtimeMonitoring", "DisableBehaviorMonitoring", "DisableOnAccessProtection"} {
			if enabled {
				errs.add(name, kReal.SetDWordValue(name, 0))
			} else {
				errs.add(name, kReal.DeleteValue(name))
			}
		}
		kReal.Close()
	}
	return errs.err()
}

// applyPowerShellPolicy restricts interactive PowerShell script execution
func applyPowerShellPolicy(disabled bool) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\Policies\Microsoft\Windows\PowerShell`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	var errs regErrs
	if disabled {
		errs.add("EnableScripts", k.SetDWordValue("EnableScripts", 0))
		errs.add("ExecutionPolicy", k.SetStringValue("ExecutionPolicy", "Restricted"))
	} else {
		errs.add("EnableScripts", k.DeleteValue("EnableScripts"))
		errs.add("ExecutionPolicy", k.DeleteValue("ExecutionPolicy"))
	}
	return errs.err()
}

// applyAutoplayPolicy restricts media and USB autorun/autoplay
func applyAutoplayPolicy(disabled bool) error {
	var errs regErrs
	for _, p := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\Explorer`,
		`SOFTWARE\Policies\Microsoft\Windows\Explorer`,
	} {
		k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, p, registry.SET_VALUE)
		if err != nil {
			errs.add("open "+p, err)
			continue
		}
		if disabled {
			errs.add("NoDriveTypeAutoRun", k.SetDWordValue("NoDriveTypeAutoRun", 0xFF)) // 255 = disable for all drives
			errs.add("NoAutoplayfornonVolume", k.SetDWordValue("NoAutoplayfornonVolume", 1))
		} else {
			errs.add("NoDriveTypeAutoRun", k.DeleteValue("NoDriveTypeAutoRun"))
			errs.add("NoAutoplayfornonVolume", k.DeleteValue("NoAutoplayfornonVolume"))
		}
		k.Close()
	}
	return errs.err()
}

// applyScreenSaverPolicy configures screen saver security and password protection on resume
func applyScreenSaverPolicy(timeoutMinutes int, passwordRequired bool) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\Policies\Microsoft\Windows\Control Panel\Desktop`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if timeoutMinutes > 0 {
		_ = k.SetStringValue("ScreenSaveActive", "1")
		_ = k.SetStringValue("ScreenSaveTimeOut", fmt.Sprintf("%d", timeoutMinutes*60))
	} else {
		_ = k.DeleteValue("ScreenSaveTimeOut")
	}

	if passwordRequired {
		_ = k.SetStringValue("ScreenSaveActive", "1")
		_ = k.SetStringValue("ScreenSaverIsSecure", "1")
	} else {
		_ = k.DeleteValue("ScreenSaverIsSecure")
	}

	if timeoutMinutes == 0 && !passwordRequired {
		_ = k.DeleteValue("ScreenSaveActive")
	}
	return nil
}

// applyAdminAccountPolicy provisions or updates a managed local administrator account.
// Passwords are submitted via stdin to PowerShell to avoid process command-line exposure.
func applyAdminAccountPolicy(s PolicySettings) error {
	if !s.AdminAccountEnabled || s.AdminAccountUsername == "" || s.AdminAccountPassword == "" {
		return nil
	}

	username := strings.TrimSpace(s.AdminAccountUsername)
	for _, r := range username {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return fmt.Errorf("invalid admin username: '%s'", username)
		}
	}

	fullName := s.AdminAccountFullName
	if fullName == "" {
		fullName = "Managed Support Administrator"
	}
	desc := s.AdminAccountDescription
	if desc == "" {
		desc = "Central IT Managed Administrator Account"
	}

	neverExpiresStr := "$false"
	if s.AdminAccountNeverExpires {
		neverExpiresStr = "$true"
	}

	// PowerShell script using stdin to read password securely.
	// Resolves S-1-5-32-544 to correctly identify Administrators group across all Windows language localizations.
	psScript := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$rawPass = [Console]::In.ReadLine()
$secPass = ConvertTo-SecureString $rawPass -AsPlainText -Force
$adminGroup = ([System.Security.Principal.SecurityIdentifier]'S-1-5-32-544').Translate([System.Security.Principal.NTAccount]).Value.Split('\')[-1]
if (Get-Command New-LocalUser -ErrorAction SilentlyContinue) {
    $u = Get-LocalUser -Name '%s' -ErrorAction SilentlyContinue
    if (-not $u) {
        New-LocalUser -Name '%s' -Password $secPass -FullName '%s' -Description '%s' -PasswordNeverExpires:%s
    } else {
        Set-LocalUser -Name '%s' -Password $secPass -FullName '%s' -Description '%s' -PasswordNeverExpires:%s
        Enable-LocalUser -Name '%s'
    }
    Add-LocalGroupMember -Group $adminGroup -Member '%s' -ErrorAction SilentlyContinue
} else {
    & net.exe user '%s' $rawPass /add /comment:'%s' /fullname:'%s' 2>$null
    if ($LASTEXITCODE -ne 0) {
        & net.exe user '%s' $rawPass /active:yes 2>$null
    }
    & net.exe localgroup $adminGroup '%s' /add 2>$null
}
`,
		username,
		username, fullName, desc, neverExpiresStr,
		username, fullName, desc, neverExpiresStr,
		username,
		username,
		username, desc, fullName,
		username,
		username,
	)

	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", psScript)
	cmd.Stdin = strings.NewReader(s.AdminAccountPassword + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("powershell provision admin user '%s': %w (output: %s)", username, err, strings.TrimSpace(string(out)))
	}

	return nil
}

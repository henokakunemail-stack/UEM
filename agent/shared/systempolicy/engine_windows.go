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

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

type winBackend struct{}

func newPlatformBackend() platformBackend {
	return &winBackend{}
}

// policyKey is the part of a registry key the policy writers use.
// registry.Key satisfies it; tests substitute a fake, so the failure paths are
// exercised without touching this machine's registry.
type policyKey interface {
	SetDWordValue(name string, value uint32) error
	SetStringValue(name, value string) error
	DeleteValue(name string) error
	Close() error
}

// openPolicyKey opens (creating if needed) a key for writing.
var openPolicyKey = func(root registry.Key, path string) (policyKey, error) {
	k, _, err := registry.CreateKey(root, path, registry.SET_VALUE)
	if err != nil {
		return nil, err
	}
	return k, nil
}

// openExistingKey opens a key that must already exist (a driver's service key).
var openExistingKey = func(root registry.Key, path string) (policyKey, error) {
	k, err := registry.OpenKey(root, path, registry.SET_VALUE)
	if err != nil {
		return nil, err
	}
	return k, nil
}

// runCmd and ejectUSB are the side effects beyond the registry, replaceable in
// tests for the same reason.
var (
	runCmd   = func(name string, args ...string) error { return exec.Command(name, args...).Run() }
	ejectUSB = ejectMountedUSBStorage
)

func (w *winBackend) Apply(ctx context.Context, client *http.Client, serverURL string, s PolicySettings) ([]protocol.SettingResult, error) {
	var out []protocol.SettingResult

	// USB storage: mass storage only; HID mice and keyboards are untouched.
	out = append(out, outcome(protocol.SettingUSBStorageMode, applyUSBStoragePolicy(s.USBStorageMode)))

	if s.WallpaperEnabled && s.WallpaperURL != "" {
		out = append(out, outcome(protocol.SettingWallpaperEnabled,
			applyWallpaper(client, serverURL, s.WallpaperURL, s.WallpaperStyle, s.WallpaperLocked)))
	} else {
		revertWallpaper()
		out = append(out, applied(protocol.SettingWallpaperEnabled))
	}

	out = append(out, outcome(protocol.SettingInactivityTimeoutMinutes, applyInactivityLock(s.InactivityTimeoutMinutes)))

	// Each restriction is its own setting, so one refused value no longer
	// marks the other three as failed too.
	restrictions := applyRestrictions(s)
	for _, key := range []string{protocol.SettingDisableTaskMgr, protocol.SettingDisableCMD,
		protocol.SettingDisableRegistryTools, protocol.SettingDisableControlPanel} {
		out = append(out, outcome(key, restrictions[key]))
	}

	out = append(out,
		outcome(protocol.SettingWindowsUpdateMode, applyWindowsUpdate(s.WindowsUpdateMode)),
		outcome(protocol.SettingFirewallEnabled, applyFirewall(s.FirewallEnabled)),
		outcome(protocol.SettingDefenderProtectionEnabled, applyDefenderProtection(s.DefenderProtectionEnabled)),
		outcome(protocol.SettingDisablePowerShell, applyPowerShellPolicy(s.DisablePowerShell)),
		outcome(protocol.SettingDisableAutoplay, applyAutoplayPolicy(s.DisableAutoplay)),
	)

	screensaver := applyScreenSaverPolicy(s.ScreenSaverTimeoutMinutes, s.ScreenSaverPasswordRequired)
	out = append(out,
		outcome(protocol.SettingScreenSaverTimeoutMinutes, screensaver),
		outcome(protocol.SettingScreenSaverPasswordRequired, screensaver),
	)

	if s.AdminAccountEnabled && s.AdminAccountUsername != "" && s.AdminAccountPassword != "" {
		out = append(out, outcome(protocol.SettingAdminAccountEnabled, applyAdminAccountPolicy(s)))
	} else {
		out = append(out, applied(protocol.SettingAdminAccountEnabled))
	}
	return out, nil
}

// applyUSBStoragePolicy configures USB mass storage driver and Removable Storage Group Policy.
// Mouse and keyboard use hidusb.sys, kbdclass.sys, mouclass.sys and remain 100% active.
func applyUSBStoragePolicy(mode USBStorageMode) error {
	var errs regErrs

	// A. Service driver Start: 3=Demand/Enabled, 4=Disabled
	if usbstorKey, err := openExistingKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\USBSTOR`); err != nil {
		errs.add("open USBSTOR key", err)
	} else {
		start := uint32(3)
		if mode == USBStorageBlock {
			start = 4
		}
		errs.add(fmt.Sprintf("set USBSTOR Start=%d", start), usbstorKey.SetDWordValue("Start", start))
		usbstorKey.Close()
	}

	// B. RemovableStorageDevices policy for Disk Drives GUID {53f5630d-b6bf-11d0-94f2-00a0c91efb8b}.
	// These writes were discarded, so a refused Deny_Write still reported the
	// read-only mode as applied.
	if removableKey, err := openPolicyKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Policies\Microsoft\Windows\RemovableStorageDevices\{53f5630d-b6bf-11d0-94f2-00a0c91efb8b}`); err != nil {
		errs.add("create RemovableStorageDevices key", err)
	} else {
		deny := map[string]uint32{}
		switch mode {
		case USBStorageBlock:
			deny = map[string]uint32{"Deny_Read": 1, "Deny_Write": 1, "Deny_Execute": 1}
		case USBStorageReadOnly:
			deny = map[string]uint32{"Deny_Read": 0, "Deny_Write": 1, "Deny_Execute": 0}
		}
		for _, name := range []string{"Deny_Read", "Deny_Write", "Deny_Execute"} {
			if v, ok := deny[name]; ok {
				errs.add(name, removableKey.SetDWordValue(name, v))
			} else {
				errs.add(name, removableKey.DeleteValue(name))
			}
		}
		removableKey.Close()
	}

	// C. StorageDevicePolicies WriteProtect (for USB read-only mode)
	if storageKey, err := openPolicyKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\StorageDevicePolicies`); err != nil {
		errs.add("open StorageDevicePolicies key", err)
	} else {
		protect := uint32(0)
		if mode == USBStorageReadOnly {
			protect = 1
		}
		errs.add("WriteProtect", storageKey.SetDWordValue("WriteProtect", protect))
		storageKey.Close()
	}

	if err := errs.err(); err != nil {
		return err
	}

	// D. Force Group Policy refresh so RemovableStorageDevices takes effect immediately
	// (without this, policy only applies on next GP cycle ~90 min or reboot). Only
	// once the policy is actually written: refreshing a half-written policy
	// enforces whatever half got through.
	_ = runCmd("gpupdate", "/force", "/wait:0")

	// E. Eject currently mounted USB storage drives so block is enforced instantly
	if mode == USBStorageBlock {
		ejectUSB()
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

	// The registry writes below used to be discarded, so a wallpaper the policy
	// key refused still reported as enforced.
	var errs regErrs

	// Set GPO Group Policy registry key in Policies\System
	if sysPolKey, err := openPolicyKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`); err != nil {
		errs.add("open Policies\\System", err)
	} else {
		errs.add("Wallpaper", sysPolKey.SetStringValue("Wallpaper", destPath))
		errs.add("WallpaperStyle", sysPolKey.SetStringValue("WallpaperStyle", styleCode))
		sysPolKey.Close()
	}

	// Lock or unlock wallpaper changes via GPO
	for _, keyPath := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\ActiveDesktop`,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`,
	} {
		k, err := openPolicyKey(registry.LOCAL_MACHINE, keyPath)
		if err != nil {
			errs.add("open "+keyPath, err)
			continue
		}
		for _, name := range []string{"NoChangingWallPaper", "NoChangingWallpaper"} {
			if locked {
				errs.add(name, k.SetDWordValue(name, 1))
			} else {
				errs.add(name, k.DeleteValue(name))
			}
		}
		k.Close()
	}

	// Set Default user desktop for new logins
	if defDesktopKey, err := openPolicyKey(registry.USERS, `.DEFAULT\Control Panel\Desktop`); err != nil {
		errs.add("open .DEFAULT desktop", err)
	} else {
		errs.add("default Wallpaper", defDesktopKey.SetStringValue("Wallpaper", destPath))
		errs.add("default WallpaperStyle", defDesktopKey.SetStringValue("WallpaperStyle", styleCode))
		errs.add("default TileWallpaper", defDesktopKey.SetStringValue("TileWallpaper", tileCode))
		defDesktopKey.Close()
	}
	if err := errs.err(); err != nil {
		return err
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
	k, err := openPolicyKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`)
	if err != nil {
		return err
	}
	defer k.Close()

	var errs regErrs
	if minutes > 0 {
		errs.add("InactivityTimeoutSecs", k.SetDWordValue("InactivityTimeoutSecs", uint32(minutes*60)))
	} else {
		errs.add("InactivityTimeoutSecs", k.DeleteValue("InactivityTimeoutSecs"))
	}
	return errs.err()
}

// applyRestrictions writes the four restriction settings and returns the
// error of each by setting key (nil when it applied). Every write used to be
// discarded through setOrDeleteDWord, so a refused DisableTaskMgr reported the
// restrictions as enforced.
func applyRestrictions(s PolicySettings) map[string]error {
	errs := map[string]*regErrs{
		protocol.SettingDisableTaskMgr:       {},
		protocol.SettingDisableRegistryTools: {},
		protocol.SettingDisableCMD:           {},
		protocol.SettingDisableControlPanel:  {},
	}

	// 1. Task Manager & Registry Editor
	if kSys, err := openPolicyKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`); err != nil {
		errs[protocol.SettingDisableTaskMgr].add("open Policies\\System", err)
		errs[protocol.SettingDisableRegistryTools].add("open Policies\\System", err)
	} else {
		errs[protocol.SettingDisableTaskMgr].add("DisableTaskMgr", setOrDeleteDWord(kSys, "DisableTaskMgr", s.DisableTaskMgr, 1))
		errs[protocol.SettingDisableRegistryTools].add("DisableRegistryTools", setOrDeleteDWord(kSys, "DisableRegistryTools", s.DisableRegistryTools, 1))
		kSys.Close()
	}

	// 2. Command Prompt (value 2 = disable interactive CMD, allow batch files so agent scripts continue)
	if kCmd, err := openPolicyKey(registry.LOCAL_MACHINE, `SOFTWARE\Policies\Microsoft\Windows\System`); err != nil {
		errs[protocol.SettingDisableCMD].add("open Policies\\Windows\\System", err)
	} else {
		errs[protocol.SettingDisableCMD].add("DisableCMD", setOrDeleteDWord(kCmd, "DisableCMD", s.DisableCMD, 2))
		kCmd.Close()
	}

	// 3. Control Panel & PC Settings (set both keys for classic and modern Windows 10/11 coverage)
	for _, p := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\Explorer`,
		`SOFTWARE\Policies\Microsoft\Windows\Explorer`,
	} {
		kExp, err := openPolicyKey(registry.LOCAL_MACHINE, p)
		if err != nil {
			errs[protocol.SettingDisableControlPanel].add("open "+p, err)
			continue
		}
		errs[protocol.SettingDisableControlPanel].add("NoControlPanel", setOrDeleteDWord(kExp, "NoControlPanel", s.DisableControlPanel, 1))
		kExp.Close()
	}

	out := make(map[string]error, len(errs))
	for key, e := range errs {
		out[key] = e.err()
	}
	return out
}

func setOrDeleteDWord(k policyKey, name string, enable bool, val uint32) error {
	if enable {
		return k.SetDWordValue(name, val)
	}
	return k.DeleteValue(name)
}

func applyWindowsUpdate(mode WindowsUpdateMode) error {
	k, err := openPolicyKey(registry.LOCAL_MACHINE, `SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU`)
	if err != nil {
		return err
	}
	defer k.Close()

	// These writes were discarded: a refused AUOptions still reported the
	// update policy as applied.
	var errs regErrs
	switch mode {
	case WindowsUpdateAutoInstall:
		errs.add("NoAutoUpdate", k.SetDWordValue("NoAutoUpdate", 0))
		errs.add("AUOptions", k.SetDWordValue("AUOptions", 4)) // Auto download and schedule install
	case WindowsUpdateNotifyOnly:
		errs.add("NoAutoUpdate", k.SetDWordValue("NoAutoUpdate", 0))
		errs.add("AUOptions", k.SetDWordValue("AUOptions", 2)) // Notify before download
	default:
		errs.add("NoAutoUpdate", k.DeleteValue("NoAutoUpdate"))
		errs.add("AUOptions", k.DeleteValue("AUOptions"))
	}
	return errs.err()
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
	k, err := openPolicyKey(registry.LOCAL_MACHINE, `SOFTWARE\Policies\Microsoft\Windows\Control Panel\Desktop`)
	if err != nil {
		return err
	}
	defer k.Close()

	// These writes were discarded, so a refused ScreenSaverIsSecure reported
	// the password-on-resume rule as enforced.
	var errs regErrs
	if timeoutMinutes > 0 {
		errs.add("ScreenSaveActive", k.SetStringValue("ScreenSaveActive", "1"))
		errs.add("ScreenSaveTimeOut", k.SetStringValue("ScreenSaveTimeOut", fmt.Sprintf("%d", timeoutMinutes*60)))
	} else {
		errs.add("ScreenSaveTimeOut", k.DeleteValue("ScreenSaveTimeOut"))
	}

	if passwordRequired {
		errs.add("ScreenSaveActive", k.SetStringValue("ScreenSaveActive", "1"))
		errs.add("ScreenSaverIsSecure", k.SetStringValue("ScreenSaverIsSecure", "1"))
	} else {
		errs.add("ScreenSaverIsSecure", k.DeleteValue("ScreenSaverIsSecure"))
	}

	if timeoutMinutes == 0 && !passwordRequired {
		errs.add("ScreenSaveActive", k.DeleteValue("ScreenSaveActive"))
	}
	return errs.err()
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

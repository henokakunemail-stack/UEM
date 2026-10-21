package systempolicy

import (
	"encoding/json"
	"time"
)

// USBStorageMode defines how USB storage devices are handled.
type USBStorageMode string

const (
	USBStorageAllow    USBStorageMode = "allow"     // Normal USB storage access
	USBStorageBlock    USBStorageMode = "block"     // Block mass storage (USB flash/HDD), allow mouse/keyboard
	USBStorageReadOnly USBStorageMode = "read_only" // Read-only access to USB flash/HDD
)

// WallpaperStyle defines how wallpaper is fitted on the screen.
type WallpaperStyle string

const (
	WallpaperFill    WallpaperStyle = "fill"    // Windows style 10
	WallpaperFit     WallpaperStyle = "fit"     // Windows style 6
	WallpaperStretch WallpaperStyle = "stretch" // Windows style 2
	WallpaperCenter  WallpaperStyle = "center"  // Windows style 0
	WallpaperTile    WallpaperStyle = "tile"    // Windows style 1
)

// WindowsUpdateMode defines how Windows updates are enforced.
type WindowsUpdateMode string

const (
	WindowsUpdateDefault     WindowsUpdateMode = "default"      // Windows default
	WindowsUpdateAutoInstall WindowsUpdateMode = "auto_install" // AUOptions = 4 (Auto download and schedule install)
	WindowsUpdateNotifyOnly  WindowsUpdateMode = "notify_only"  // AUOptions = 2 (Notify before download)
)

// PolicySettings defines the configurable settings in a system policy.
type PolicySettings struct {
	// Hardware & Peripherals
	USBStorageMode USBStorageMode `json:"usb_storage_mode"` // "allow", "block", "read_only"

	// Desktop Personalization
	WallpaperEnabled bool           `json:"wallpaper_enabled"`
	WallpaperURL     string         `json:"wallpaper_url,omitempty"`
	WallpaperStyle   WallpaperStyle `json:"wallpaper_style,omitempty"` // "fill", "fit", "stretch", "center", "tile"
	WallpaperLocked  bool           `json:"wallpaper_locked"`          // Lock wallpaper to prevent user changes

	// Firewall & Network Security
	FirewallEnabled bool `json:"firewall_enabled"` // Enforce Windows Defender Firewall on all profiles

	// Windows Defender Real-time Protection
	DefenderProtectionEnabled bool `json:"defender_protection_enabled"` // Prevent disabling Windows Defender real-time protection

	// Disable PowerShell / Script Execution
	DisablePowerShell bool `json:"disable_powershell"` // Block PowerShell execution

	// Disable USB Autoplay / Autorun
	DisableAutoplay bool `json:"disable_autoplay"` // Disable Windows Autoplay for all drive types

	// Screen Saver
	ScreenSaverTimeoutMinutes   int  `json:"screensaver_timeout_minutes"`   // 0 = disabled, non-zero enables timeout
	ScreenSaverPasswordRequired bool `json:"screensaver_password_required"` // Require password on screen saver resume

	// Security & Access Restrictions
	InactivityTimeoutMinutes int               `json:"inactivity_timeout_minutes"` // 0 = disabled, 5, 10, 15, 30, 60
	DisableTaskMgr           bool              `json:"disable_task_mgr"`
	DisableCMD               bool              `json:"disable_cmd"`
	DisableRegistryTools     bool              `json:"disable_registry_tools"`
	DisableControlPanel      bool              `json:"disable_control_panel"`
	WindowsUpdateMode        WindowsUpdateMode `json:"windows_update_mode"` // "default", "auto_install", "notify_only"

	// Managed Local Administrator Account
	AdminAccountEnabled  bool   `json:"admin_account_enabled"`
	AdminAccountUsername string `json:"admin_account_username,omitempty"`
	AdminAccountPassword string `json:"admin_account_password,omitempty"`
	// AdminAccountPasswordSet is what the console sees instead of the password:
	// whether one is stored. Never sent to agents as meaningful input.
	AdminAccountPasswordSet  bool   `json:"admin_account_password_set,omitempty"`
	AdminAccountFullName     string `json:"admin_account_full_name,omitempty"`
	AdminAccountDescription  string `json:"admin_account_description,omitempty"`
	AdminAccountNeverExpires bool   `json:"admin_account_never_expires"`
}

// SystemPolicy represents an AD GPO-like endpoint policy stored in the database.
type SystemPolicy struct {
	ID          string         `json:"id" db:"id"`
	Name        string         `json:"name" db:"name"`
	Description string         `json:"description" db:"description"`
	TargetType  string         `json:"target_type" db:"target_type"` // 'all', 'group', 'device'
	TargetID    string         `json:"target_id" db:"target_id"`
	IsEnabled   bool           `json:"is_enabled" db:"is_enabled"`
	Priority    int            `json:"priority" db:"priority"`
	Settings    PolicySettings `json:"settings"`
	CreatedBy   string         `json:"created_by" db:"created_by"`
	CreatedAt   time.Time      `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at" db:"updated_at"`

	// Raw JSON from database
	SettingsJSON string `json:"-" db:"settings_json"`
}

// UnmarshalSettings unpacks SettingsJSON into Settings.
func (p *SystemPolicy) UnmarshalSettings() error {
	if p.SettingsJSON == "" || p.SettingsJSON == "{}" {
		p.Settings = PolicySettings{
			USBStorageMode:    USBStorageAllow,
			WallpaperStyle:    WallpaperFill,
			WindowsUpdateMode: WindowsUpdateDefault,
		}
		return nil
	}
	return json.Unmarshal([]byte(p.SettingsJSON), &p.Settings)
}

// MarshalSettings packs Settings into SettingsJSON.
func (p *SystemPolicy) MarshalSettings() error {
	b, err := json.Marshal(p.Settings)
	if err != nil {
		return err
	}
	p.SettingsJSON = string(b)
	return nil
}

// CompiledPolicy represents the effective merged policy for an endpoint device.
type CompiledPolicy struct {
	DeviceID string         `json:"device_id"`
	Version  string         `json:"version"` // SHA-256 hash of effective settings (first 16 hex chars)
	Settings PolicySettings `json:"settings"`
}

// DeviceSystemPolicyState tracks the sync and enforcement state for a device.
type DeviceSystemPolicyState struct {
	DeviceID            string         `json:"device_id" db:"device_id"`
	PolicyVersion       string         `json:"policy_version" db:"policy_version"`
	Status              string         `json:"status" db:"status"` // 'synced', 'pending', 'failed'
	AppliedSettings     PolicySettings `json:"applied_settings"`
	AppliedSettingsJSON string         `json:"-" db:"applied_settings_json"`
	LastAppliedAt       time.Time      `json:"last_applied_at" db:"last_applied_at"`
	ErrorMessage        string         `json:"error_message" db:"error_message"`
}

// PolicyReportRequest is the payload sent by the agent when reporting policy enforcement.
type PolicyReportRequest struct {
	PolicyVersion string         `json:"policy_version"`
	Status        string         `json:"status"` // "synced", "failed"
	Settings      PolicySettings `json:"settings"`
	ErrorMessage  string         `json:"error_message,omitempty"`
}

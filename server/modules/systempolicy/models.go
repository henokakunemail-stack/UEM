package systempolicy

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
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

// settingsVersion marks a stored PolicyConfig. A blob without it was written
// by an earlier server, whose console and handlers filled every field with a
// default, so its "allow", false and 0 values are not decisions.
const settingsVersion = 2

// PolicyConfig is what one policy says. A nil decision field is "not set":
// the policy leaves that setting to broader or lower-priority policies, and to
// the OS default when nobody decides it.
//
// Policies used to store plain values. The handlers and the console filled
// every field, so each policy carried an explicit usb_storage_mode "allow", and
// any policy that ranked above a USB block lifted it -- including one written
// only to set a wallpaper. A field now decides something only when it is set.
//
// Attributes (wallpaper URL/style/lock, the admin account's details) belong to
// the decision that turns them on and are read only when it is set to true.
type PolicyConfig struct {
	SettingsVersion int `json:"settings_version"`

	USBStorageMode *USBStorageMode `json:"usb_storage_mode"`

	WallpaperEnabled *bool          `json:"wallpaper_enabled"`
	WallpaperURL     string         `json:"wallpaper_url,omitempty"`
	WallpaperStyle   WallpaperStyle `json:"wallpaper_style,omitempty"`
	WallpaperLocked  bool           `json:"wallpaper_locked"`

	FirewallEnabled             *bool `json:"firewall_enabled"`
	DefenderProtectionEnabled   *bool `json:"defender_protection_enabled"`
	DisablePowerShell           *bool `json:"disable_powershell"`
	DisableAutoplay             *bool `json:"disable_autoplay"`
	ScreenSaverTimeoutMinutes   *int  `json:"screensaver_timeout_minutes"`
	ScreenSaverPasswordRequired *bool `json:"screensaver_password_required"`

	InactivityTimeoutMinutes *int               `json:"inactivity_timeout_minutes"`
	DisableTaskMgr           *bool              `json:"disable_task_mgr"`
	DisableCMD               *bool              `json:"disable_cmd"`
	DisableRegistryTools     *bool              `json:"disable_registry_tools"`
	DisableControlPanel      *bool              `json:"disable_control_panel"`
	WindowsUpdateMode        *WindowsUpdateMode `json:"windows_update_mode"`

	AdminAccountEnabled      *bool  `json:"admin_account_enabled"`
	AdminAccountUsername     string `json:"admin_account_username,omitempty"`
	AdminAccountPassword     string `json:"admin_account_password,omitempty"`
	AdminAccountPasswordSet  bool   `json:"admin_account_password_set,omitempty"`
	AdminAccountFullName     string `json:"admin_account_full_name,omitempty"`
	AdminAccountDescription  string `json:"admin_account_description,omitempty"`
	AdminAccountNeverExpires bool   `json:"admin_account_never_expires"`
}

func ptr[T any](v T) *T { return &v }

// trueOnly keeps a legacy boolean only when it said "on".
func trueOnly(b bool) *bool {
	if b {
		return ptr(true)
	}
	return nil
}

// positiveOnly keeps a legacy minute count only when it enabled something.
func positiveOnly(n int) *int {
	if n > 0 {
		return ptr(n)
	}
	return nil
}

// fromLegacy reads a policy written before settings were tri-state. Its
// "allow", "default", false and 0 values were filled in by the old console and
// handlers whether or not the operator chose them, so they become "not set";
// anything that turned a restriction on is kept as an explicit decision.
func fromLegacy(s PolicySettings) PolicyConfig {
	c := PolicyConfig{
		SettingsVersion:             settingsVersion,
		FirewallEnabled:             trueOnly(s.FirewallEnabled),
		DefenderProtectionEnabled:   trueOnly(s.DefenderProtectionEnabled),
		DisablePowerShell:           trueOnly(s.DisablePowerShell),
		DisableAutoplay:             trueOnly(s.DisableAutoplay),
		ScreenSaverTimeoutMinutes:   positiveOnly(s.ScreenSaverTimeoutMinutes),
		ScreenSaverPasswordRequired: trueOnly(s.ScreenSaverPasswordRequired),
		InactivityTimeoutMinutes:    positiveOnly(s.InactivityTimeoutMinutes),
		DisableTaskMgr:              trueOnly(s.DisableTaskMgr),
		DisableCMD:                  trueOnly(s.DisableCMD),
		DisableRegistryTools:        trueOnly(s.DisableRegistryTools),
		DisableControlPanel:         trueOnly(s.DisableControlPanel),
	}
	if s.USBStorageMode == USBStorageBlock || s.USBStorageMode == USBStorageReadOnly {
		c.USBStorageMode = ptr(s.USBStorageMode)
	}
	if s.WindowsUpdateMode == WindowsUpdateAutoInstall || s.WindowsUpdateMode == WindowsUpdateNotifyOnly {
		c.WindowsUpdateMode = ptr(s.WindowsUpdateMode)
	}
	if s.WallpaperEnabled {
		c.WallpaperEnabled = ptr(true)
		c.WallpaperURL = s.WallpaperURL
		c.WallpaperStyle = s.WallpaperStyle
		c.WallpaperLocked = s.WallpaperLocked
	}
	if s.AdminAccountEnabled {
		c.AdminAccountEnabled = ptr(true)
		c.AdminAccountUsername = s.AdminAccountUsername
		c.AdminAccountPassword = s.AdminAccountPassword
		c.AdminAccountFullName = s.AdminAccountFullName
		c.AdminAccountDescription = s.AdminAccountDescription
		c.AdminAccountNeverExpires = s.AdminAccountNeverExpires
	}
	return c
}

// decodePolicyConfig reads stored settings or a request body: a tri-state blob
// as written, anything else through fromLegacy. A console tab opened before an
// upgrade still posts the old shape, and its auto-filled "allow" must not
// become an explicit decision.
func decodePolicyConfig(raw []byte) (PolicyConfig, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return PolicyConfig{SettingsVersion: settingsVersion}, nil
	}
	var probe struct {
		Version int `json:"settings_version"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return PolicyConfig{}, err
	}
	if probe.Version >= settingsVersion {
		var c PolicyConfig
		if err := json.Unmarshal(trimmed, &c); err != nil {
			return PolicyConfig{}, err
		}
		c.SettingsVersion = settingsVersion
		return c, nil
	}
	var legacy PolicySettings
	if err := json.Unmarshal(trimmed, &legacy); err != nil {
		return PolicyConfig{}, err
	}
	return fromLegacy(legacy), nil
}

// SystemPolicy represents an AD GPO-like endpoint policy stored in the database.
type SystemPolicy struct {
	ID          string       `json:"id" db:"id"`
	Name        string       `json:"name" db:"name"`
	Description string       `json:"description" db:"description"`
	TargetType  string       `json:"target_type" db:"target_type"` // 'all', 'group', 'device'
	TargetID    string       `json:"target_id" db:"target_id"`
	IsEnabled   bool         `json:"is_enabled" db:"is_enabled"`
	Priority    int          `json:"priority" db:"priority"`
	Settings    PolicyConfig `json:"settings"`
	CreatedBy   string       `json:"created_by" db:"created_by"`
	CreatedAt   time.Time    `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at" db:"updated_at"`

	// Raw JSON from database
	SettingsJSON string `json:"-" db:"settings_json"`
}

// UnmarshalSettings unpacks SettingsJSON into Settings.
func (p *SystemPolicy) UnmarshalSettings() error {
	c, err := decodePolicyConfig([]byte(p.SettingsJSON))
	if err != nil {
		return err
	}
	p.Settings = c
	return nil
}

// MarshalSettings packs Settings into SettingsJSON, always as a tri-state blob.
func (p *SystemPolicy) MarshalSettings() error {
	p.Settings.SettingsVersion = settingsVersion
	b, err := json.Marshal(p.Settings)
	if err != nil {
		return err
	}
	p.SettingsJSON = string(b)
	return nil
}

// SettingSource names the policy that decided one setting for a device.
type SettingSource struct {
	PolicyID   string `json:"policy_id"`
	PolicyName string `json:"policy_name"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Priority   int    `json:"priority"`
}

// PolicyRef is one policy that applies to a device, listed most decisive first.
type PolicyRef = SettingSource

// CompiledPolicy represents the effective merged policy for an endpoint device.
type CompiledPolicy struct {
	DeviceID string         `json:"device_id"`
	Version  string         `json:"version"` // SHA-256 hash of effective settings (first 16 hex chars)
	Settings PolicySettings `json:"settings"`
}

// Device policy states. 'partial' means the agent applied the policy but some
// settings failed or are not supported on that OS; Results says which.
const (
	statePending = "pending"
	stateSynced  = "synced"
	statePartial = "partial"
	stateFailed  = "failed"
)

// DeviceSystemPolicyState tracks the sync and enforcement state for a device.
type DeviceSystemPolicyState struct {
	DeviceID            string                   `json:"device_id" db:"device_id"`
	PolicyVersion       string                   `json:"policy_version" db:"policy_version"`
	Status              string                   `json:"status" db:"status"` // pending, synced, partial, failed
	AppliedSettings     PolicySettings           `json:"applied_settings"`
	AppliedSettingsJSON string                   `json:"-" db:"applied_settings_json"`
	Results             []protocol.SettingResult `json:"results"`
	ResultsJSON         string                   `json:"-" db:"results_json"`
	LastAppliedAt       time.Time                `json:"last_applied_at" db:"last_applied_at"`
	ErrorMessage        string                   `json:"error_message" db:"error_message"`
}

// PolicyReportRequest is the payload sent by the agent when reporting policy enforcement.
type PolicyReportRequest struct {
	PolicyVersion string         `json:"policy_version"`
	Status        string         `json:"status"` // synced, partial, failed
	Settings      PolicySettings `json:"settings"`
	ErrorMessage  string         `json:"error_message,omitempty"`
	// Results is what the agent did with each setting. Agents before it send
	// none, and their Status is taken as reported.
	Results []protocol.SettingResult `json:"results,omitempty"`
}

package protocol

// System policy settings, as named on the wire. Each key is one decision a
// policy can make; a setting's attributes (wallpaper URL and style, the admin
// account's name and password) travel with the key that turns it on.
//
// The server reports which policy decided each key, and the agent reports what
// it did with each key, so both halves have to spell them the same way.
const (
	SettingUSBStorageMode              = "usb_storage_mode"
	SettingWallpaperEnabled            = "wallpaper_enabled"
	SettingInactivityTimeoutMinutes    = "inactivity_timeout_minutes"
	SettingDisableTaskMgr              = "disable_task_mgr"
	SettingDisableCMD                  = "disable_cmd"
	SettingDisableRegistryTools        = "disable_registry_tools"
	SettingDisableControlPanel         = "disable_control_panel"
	SettingWindowsUpdateMode           = "windows_update_mode"
	SettingFirewallEnabled             = "firewall_enabled"
	SettingDefenderProtectionEnabled   = "defender_protection_enabled"
	SettingDisablePowerShell           = "disable_powershell"
	SettingDisableAutoplay             = "disable_autoplay"
	SettingScreenSaverTimeoutMinutes   = "screensaver_timeout_minutes"
	SettingScreenSaverPasswordRequired = "screensaver_password_required"
	SettingAdminAccountEnabled         = "admin_account_enabled"
)

// SettingKeys lists every key above in a fixed order, the order the console
// shows them in.
var SettingKeys = []string{
	SettingUSBStorageMode,
	SettingWallpaperEnabled,
	SettingInactivityTimeoutMinutes,
	SettingDisableTaskMgr,
	SettingDisableCMD,
	SettingDisableRegistryTools,
	SettingDisableControlPanel,
	SettingWindowsUpdateMode,
	SettingFirewallEnabled,
	SettingDefenderProtectionEnabled,
	SettingDisablePowerShell,
	SettingDisableAutoplay,
	SettingScreenSaverTimeoutMinutes,
	SettingScreenSaverPasswordRequired,
	SettingAdminAccountEnabled,
}

// Outcomes an agent reports for one setting.
const (
	// OutcomeApplied: the endpoint now enforces the value it was sent.
	OutcomeApplied = "applied"
	// OutcomeFailed: the agent tried and the OS refused (Reason says how).
	OutcomeFailed = "failed"
	// OutcomeUnsupported: this OS or agent has no way to enforce the setting,
	// so nothing was attempted. Reported instead of being skipped in silence.
	OutcomeUnsupported = "unsupported"
)

// SettingResult is what the agent did with one setting key.
type SettingResult struct {
	Setting string `json:"setting"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

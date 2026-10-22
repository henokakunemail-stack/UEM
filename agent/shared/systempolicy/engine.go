package systempolicy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/reportretry"
	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

type USBStorageMode string

const (
	USBStorageAllow    USBStorageMode = "allow"
	USBStorageBlock    USBStorageMode = "block"
	USBStorageReadOnly USBStorageMode = "read_only"
)

type WallpaperStyle string

const (
	WallpaperFill    WallpaperStyle = "fill"
	WallpaperFit     WallpaperStyle = "fit"
	WallpaperStretch WallpaperStyle = "stretch"
	WallpaperCenter  WallpaperStyle = "center"
	WallpaperTile    WallpaperStyle = "tile"
)

type WindowsUpdateMode string

const (
	WindowsUpdateDefault     WindowsUpdateMode = "default"
	WindowsUpdateAutoInstall WindowsUpdateMode = "auto_install"
	WindowsUpdateNotifyOnly  WindowsUpdateMode = "notify_only"
)

type PolicySettings struct {
	USBStorageMode              USBStorageMode    `json:"usb_storage_mode"`
	WallpaperEnabled            bool              `json:"wallpaper_enabled"`
	WallpaperURL                string            `json:"wallpaper_url,omitempty"`
	WallpaperStyle              WallpaperStyle    `json:"wallpaper_style,omitempty"`
	InactivityTimeoutMinutes    int               `json:"inactivity_timeout_minutes"`
	DisableTaskMgr              bool              `json:"disable_task_mgr"`
	DisableCMD                  bool              `json:"disable_cmd"`
	DisableRegistryTools        bool              `json:"disable_registry_tools"`
	DisableControlPanel         bool              `json:"disable_control_panel"`
	WindowsUpdateMode           WindowsUpdateMode `json:"windows_update_mode"`
	WallpaperLocked             bool              `json:"wallpaper_locked"`
	FirewallEnabled             bool              `json:"firewall_enabled"`
	DefenderProtectionEnabled   bool              `json:"defender_protection_enabled"`
	DisablePowerShell           bool              `json:"disable_powershell"`
	DisableAutoplay             bool              `json:"disable_autoplay"`
	ScreenSaverTimeoutMinutes   int               `json:"screensaver_timeout_minutes"`
	ScreenSaverPasswordRequired bool              `json:"screensaver_password_required"`
	AdminAccountEnabled         bool              `json:"admin_account_enabled"`
	AdminAccountUsername        string            `json:"admin_account_username,omitempty"`
	AdminAccountPassword        string            `json:"admin_account_password,omitempty"`
	AdminAccountFullName        string            `json:"admin_account_full_name,omitempty"`
	AdminAccountDescription     string            `json:"admin_account_description,omitempty"`
	AdminAccountNeverExpires    bool              `json:"admin_account_never_expires"`
}

// platformBackend applies a policy and says, setting by setting, what happened:
// applied, failed (and why), or unsupported on this OS. Backends used to return
// a count and a "degraded" string, and the Linux/macOS one skipped every
// Windows-only setting without a word, so the device reported "synced" for
// restrictions it never enforced.
type platformBackend interface {
	Apply(ctx context.Context, client *http.Client, serverURL string, settings PolicySettings) ([]protocol.SettingResult, error)
}

func applied(key string) protocol.SettingResult {
	return protocol.SettingResult{Setting: key, Outcome: protocol.OutcomeApplied}
}

func failed(key string, err error) protocol.SettingResult {
	return protocol.SettingResult{Setting: key, Outcome: protocol.OutcomeFailed, Reason: err.Error()}
}

func unsupported(key, reason string) protocol.SettingResult {
	return protocol.SettingResult{Setting: key, Outcome: protocol.OutcomeUnsupported, Reason: reason}
}

// outcome is applied when err is nil, failed otherwise.
func outcome(key string, err error) protocol.SettingResult {
	if err != nil {
		return failed(key, err)
	}
	return applied(key)
}

// Summarize reduces per-setting results to the single status a server without
// per-setting results understands: "failed" when anything failed, "synced"
// otherwise, which is what this agent reported before results existed. A
// current server ignores it and derives the status from the results.
func Summarize(results []protocol.SettingResult) (status, errMsg string) {
	var reasons []string
	for _, r := range results {
		if r.Outcome == protocol.OutcomeFailed {
			reasons = append(reasons, r.Setting+": "+r.Reason)
		}
	}
	if len(reasons) > 0 {
		return "failed", strings.Join(reasons, "; ")
	}
	return "synced", ""
}

type Engine struct {
	serverURL    string
	deviceID     string
	deviceSecret string
	httpClient   *http.Client
	mu           sync.Mutex
	backend      platformBackend
}

func NewEngine(serverURL, deviceID, deviceSecret string) *Engine {
	return &Engine{
		serverURL:    strings.TrimRight(serverURL, "/"),
		deviceID:     deviceID,
		deviceSecret: deviceSecret,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		backend: newPlatformBackend(),
	}
}

func (e *Engine) Apply(ctx context.Context, version string, settings PolicySettings) ([]protocol.SettingResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	log.Info().Str("version", version).
		Str("usb_mode", string(settings.USBStorageMode)).
		Bool("wallpaper", settings.WallpaperEnabled).
		Int("inactivity_min", settings.InactivityTimeoutMinutes).
		Msg("applying system policy")

	results, err := e.backend.Apply(ctx, e.httpClient, e.serverURL, settings)
	if err != nil {
		log.Error().Err(err).Str("version", version).Msg("apply system policy failed")
		return results, err
	}

	var nApplied, nFailed, nUnsupported int
	for _, r := range results {
		switch r.Outcome {
		case protocol.OutcomeApplied:
			nApplied++
		case protocol.OutcomeFailed:
			nFailed++
			log.Warn().Str("setting", r.Setting).Str("reason", r.Reason).Msg("system policy setting failed")
		case protocol.OutcomeUnsupported:
			nUnsupported++
		}
	}
	log.Info().Str("version", version).Int("applied", nApplied).Int("failed", nFailed).
		Int("unsupported", nUnsupported).Msg("system policy applied")
	return results, nil
}

// Report sends the outcome of one apply. It retries a transient failure: a
// lost report used to be dropped (the caller discarded the error), leaving the
// device "pending" in the console until the next reconnect re-sent the policy.
func (e *Engine) Report(ctx context.Context, version string, status string, settings PolicySettings, errMsg string, results []protocol.SettingResult) error {
	reportURL := fmt.Sprintf("%s/api/agent/devices/%s/system-policy/report", e.serverURL, e.deviceID)
	// The server needs to know what was applied, not the managed admin
	// password: it never stores that per device, so it does not travel back.
	settings.AdminAccountPassword = ""

	return reportretry.Post(ctx, e.httpClient, reportURL, e.deviceID, e.deviceSecret, map[string]any{
		"policy_version": version,
		"status":         status,
		"settings":       settings,
		"error_message":  errMsg,
		"results":        results,
	})
}

package systempolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
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
	USBStorageMode           USBStorageMode    `json:"usb_storage_mode"`
	WallpaperEnabled         bool              `json:"wallpaper_enabled"`
	WallpaperURL             string            `json:"wallpaper_url,omitempty"`
	WallpaperStyle           WallpaperStyle    `json:"wallpaper_style,omitempty"`
	InactivityTimeoutMinutes int               `json:"inactivity_timeout_minutes"`
	DisableTaskMgr           bool              `json:"disable_task_mgr"`
	DisableCMD               bool              `json:"disable_cmd"`
	DisableRegistryTools     bool              `json:"disable_registry_tools"`
	DisableControlPanel      bool              `json:"disable_control_panel"`
	WindowsUpdateMode        WindowsUpdateMode `json:"windows_update_mode"`
	WallpaperLocked          bool              `json:"wallpaper_locked"`
	FirewallEnabled          bool              `json:"firewall_enabled"`
	DefenderProtectionEnabled bool             `json:"defender_protection_enabled"`
	DisablePowerShell        bool              `json:"disable_powershell"`
	DisableAutoplay          bool              `json:"disable_autoplay"`
	ScreenSaverTimeoutMinutes int              `json:"screensaver_timeout_minutes"`
	ScreenSaverPasswordRequired bool            `json:"screensaver_password_required"`
	AdminAccountEnabled       bool             `json:"admin_account_enabled"`
	AdminAccountUsername      string           `json:"admin_account_username,omitempty"`
	AdminAccountPassword      string           `json:"admin_account_password,omitempty"`
	AdminAccountFullName      string           `json:"admin_account_full_name,omitempty"`
	AdminAccountDescription   string           `json:"admin_account_description,omitempty"`
	AdminAccountNeverExpires  bool             `json:"admin_account_never_expires"`
}

type platformBackend interface {
	Apply(ctx context.Context, client *http.Client, serverURL string, settings PolicySettings) (int, string, error)
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

func (e *Engine) Apply(ctx context.Context, version string, settings PolicySettings) (int, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	log.Info().Str("version", version).
		Str("usb_mode", string(settings.USBStorageMode)).
		Bool("wallpaper", settings.WallpaperEnabled).
		Int("inactivity_min", settings.InactivityTimeoutMinutes).
		Msg("applying system policy")

	appliedCount, degradedMsg, err := e.backend.Apply(ctx, e.httpClient, e.serverURL, settings)
	if err != nil {
		log.Error().Err(err).Str("version", version).Msg("apply system policy failed")
		return appliedCount, degradedMsg, err
	}

	log.Info().Str("version", version).Int("applied", appliedCount).Msg("system policy applied")
	return appliedCount, degradedMsg, nil
}

func (e *Engine) Report(ctx context.Context, version string, status string, settings PolicySettings, errMsg string) error {
	reportURL := fmt.Sprintf("%s/api/agent/devices/%s/system-policy/report", e.serverURL, e.deviceID)

	body, err := json.Marshal(map[string]any{
		"policy_version": version,
		"status":         status,
		"settings":       settings,
		"error_message":  errMsg,
	})
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reportURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create report request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device-Id", e.deviceID)
	req.Header.Set("X-Device-Secret", e.deviceSecret)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("report response status: %d", resp.StatusCode)
	}

	return nil
}

package systempolicy

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jmoiron/sqlx"
)

type Repository struct {
	db *sqlx.DB
	// versionKey keys the password's contribution to the policy version, so
	// the version (visible to every console role) cannot be used to test
	// password guesses offline.
	versionKey []byte
}

func NewRepository(db *sqlx.DB) *Repository {
	// A random key until WithVersionKey sets the server's: versions are then
	// only stable for this process, which costs a re-apply after a restart but
	// never leaks anything.
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &Repository{db: db, versionKey: key}
}

// WithVersionKey derives the version key from a server secret (JWT_SECRET),
// which every node of a cluster shares, so all nodes compute the same version.
func (r *Repository) WithVersionKey(secret string) *Repository {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("systempolicy/admin-password/v1"))
	r.versionKey = m.Sum(nil)
	return r
}

func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (r *Repository) List(ctx context.Context) ([]SystemPolicy, error) {
	q := `SELECT id, name, description, target_type, target_id, is_enabled, priority,
	             settings_json, created_by, created_at, updated_at
	      FROM system_policies
	      ORDER BY priority ASC, created_at DESC`
	var rows []SystemPolicy
	if err := r.db.SelectContext(ctx, &rows, q); err != nil {
		return nil, fmt.Errorf("list system policies: %w", err)
	}
	for i := range rows {
		_ = rows[i].UnmarshalSettings()
	}
	if rows == nil {
		rows = []SystemPolicy{}
	}
	return rows, nil
}

func (r *Repository) GetByID(ctx context.Context, id string) (*SystemPolicy, error) {
	q := `SELECT id, name, description, target_type, target_id, is_enabled, priority,
	             settings_json, created_by, created_at, updated_at
	      FROM system_policies WHERE id = ?`
	var p SystemPolicy
	if err := r.db.GetContext(ctx, &p, r.db.Rebind(q), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get system policy %s: %w", id, err)
	}
	_ = p.UnmarshalSettings()
	return &p, nil
}

func (r *Repository) Create(ctx context.Context, p *SystemPolicy) error {
	if p.ID == "" {
		p.ID = NewID()
	}
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	if err := p.MarshalSettings(); err != nil {
		return fmt.Errorf("marshal policy settings: %w", err)
	}

	q := `INSERT INTO system_policies (id, name, description, target_type, target_id, is_enabled, priority, settings_json, created_by, created_at, updated_at)
	      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, r.db.Rebind(q),
		p.ID, p.Name, p.Description, p.TargetType, p.TargetID,
		boolToInt(p.IsEnabled), p.Priority, p.SettingsJSON,
		p.CreatedBy, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert system policy: %w", err)
	}
	return nil
}

func (r *Repository) Update(ctx context.Context, p *SystemPolicy) error {
	p.UpdatedAt = time.Now().UTC()
	if err := p.MarshalSettings(); err != nil {
		return fmt.Errorf("marshal policy settings: %w", err)
	}

	q := `UPDATE system_policies
	      SET name = ?, description = ?, target_type = ?, target_id = ?,
	          is_enabled = ?, priority = ?, settings_json = ?, updated_at = ?
	      WHERE id = ?`
	res, err := r.db.ExecContext(ctx, r.db.Rebind(q),
		p.Name, p.Description, p.TargetType, p.TargetID,
		boolToInt(p.IsEnabled), p.Priority, p.SettingsJSON, p.UpdatedAt,
		p.ID,
	)
	if err != nil {
		return fmt.Errorf("update system policy %s: %w", p.ID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("system policy not found")
	}
	return nil
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	q := `DELETE FROM system_policies WHERE id = ?`
	_, err := r.db.ExecContext(ctx, r.db.Rebind(q), id)
	if err != nil {
		return fmt.Errorf("delete system policy %s: %w", id, err)
	}
	return nil
}

func (r *Repository) GetDeviceState(ctx context.Context, deviceID string) (*DeviceSystemPolicyState, error) {
	q := `SELECT device_id, policy_version, status, applied_settings_json, last_applied_at, error_message
	      FROM device_system_policy_states WHERE device_id = ?`
	var s DeviceSystemPolicyState
	if err := r.db.GetContext(ctx, &s, r.db.Rebind(q), deviceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get device policy state %s: %w", deviceID, err)
	}
	if s.AppliedSettingsJSON != "" && s.AppliedSettingsJSON != "{}" {
		_ = json.Unmarshal([]byte(s.AppliedSettingsJSON), &s.AppliedSettings)
	}
	return &s, nil
}

func (r *Repository) ListDeviceStates(ctx context.Context) ([]DeviceSystemPolicyState, error) {
	q := `SELECT device_id, policy_version, status, applied_settings_json, last_applied_at, error_message
	      FROM device_system_policy_states
	      ORDER BY last_applied_at DESC`
	var rows []DeviceSystemPolicyState
	if err := r.db.SelectContext(ctx, &rows, q); err != nil {
		return nil, fmt.Errorf("list device policy states: %w", err)
	}
	for i := range rows {
		if rows[i].AppliedSettingsJSON != "" && rows[i].AppliedSettingsJSON != "{}" {
			_ = json.Unmarshal([]byte(rows[i].AppliedSettingsJSON), &rows[i].AppliedSettings)
		}
	}
	if rows == nil {
		rows = []DeviceSystemPolicyState{}
	}
	return rows, nil
}

func (r *Repository) UpdateDeviceState(ctx context.Context, state *DeviceSystemPolicyState) error {
	if state.LastAppliedAt.IsZero() {
		state.LastAppliedAt = time.Now().UTC()
	}
	// The managed admin password is never stored per device: the agent's
	// report echoes the settings it applied, password included.
	settingsBytes, err := json.Marshal(state.AppliedSettings.redacted())
	if err != nil {
		settingsBytes = []byte("{}")
	}
	state.AppliedSettingsJSON = string(settingsBytes)

	// Upsert query compatible with SQLite and PostgreSQL
	q := `INSERT INTO device_system_policy_states (device_id, policy_version, status, applied_settings_json, last_applied_at, error_message)
	      VALUES (?, ?, ?, ?, ?, ?)
	      ON CONFLICT(device_id) DO UPDATE SET
	          policy_version = excluded.policy_version,
	          status = excluded.status,
	          applied_settings_json = excluded.applied_settings_json,
	          last_applied_at = excluded.last_applied_at,
	          error_message = excluded.error_message`
	_, err = r.db.ExecContext(ctx, r.db.Rebind(q),
		state.DeviceID, state.PolicyVersion, state.Status,
		state.AppliedSettingsJSON, state.LastAppliedAt, state.ErrorMessage,
	)
	if err != nil {
		return fmt.Errorf("upsert device policy state %s: %w", state.DeviceID, err)
	}
	return nil
}

// GetTargetDeviceIDs expands targetType and targetID to matching unretired device IDs.
func (r *Repository) GetTargetDeviceIDs(ctx context.Context, targetType, targetID string) ([]string, error) {
	var ids []string
	switch targetType {
	case "device":
		if targetID == "" {
			return nil, nil
		}
		var exists int
		q := `SELECT COUNT(*) FROM devices WHERE id = ? AND retired_at IS NULL`
		if err := r.db.GetContext(ctx, &exists, r.db.Rebind(q), targetID); err != nil {
			return nil, err
		}
		if exists > 0 {
			ids = append(ids, targetID)
		}
	case "group":
		if targetID == "" {
			return nil, nil
		}
		q := `SELECT m.device_id
		      FROM device_group_members m
		      INNER JOIN devices d ON d.id = m.device_id
		      WHERE m.group_id = ? AND d.retired_at IS NULL`
		if err := r.db.SelectContext(ctx, &ids, r.db.Rebind(q), targetID); err != nil {
			return nil, err
		}
	case "all":
		q := `SELECT id FROM devices WHERE retired_at IS NULL`
		if err := r.db.SelectContext(ctx, &ids, q); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// CompileEffectivePolicy resolves all applicable policies for a device,
// merges them in hierarchical priority order (device > group > all, then priority ASC),
// and calculates a deterministic SHA-256 policy version hash.
func (r *Repository) CompileEffectivePolicy(ctx context.Context, deviceID string) (*CompiledPolicy, error) {
	// 1. Get groups for device
	groupQuery := `SELECT group_id FROM device_group_members WHERE device_id = ?`
	var groupIDs []string
	if err := r.db.SelectContext(ctx, &groupIDs, r.db.Rebind(groupQuery), deviceID); err != nil {
		return nil, fmt.Errorf("lookup device groups: %w", err)
	}

	// 2. Query all enabled policies matching device, groups, or all
	var allPolicies []SystemPolicy
	baseQuery := `SELECT id, name, description, target_type, target_id, is_enabled, priority,
	                     settings_json, created_by, created_at, updated_at
	              FROM system_policies
	              WHERE is_enabled = 1`
	if err := r.db.SelectContext(ctx, &allPolicies, baseQuery); err != nil {
		return nil, fmt.Errorf("query enabled system policies: %w", err)
	}

	// Filter matching policies
	groupMap := make(map[string]bool)
	for _, g := range groupIDs {
		groupMap[g] = true
	}

	var matched []SystemPolicy
	for _, p := range allPolicies {
		switch p.TargetType {
		case "all":
			matched = append(matched, p)
		case "group":
			if groupMap[p.TargetID] {
				matched = append(matched, p)
			}
		case "device":
			if p.TargetID == deviceID {
				matched = append(matched, p)
			}
		}
	}

	for i := range matched {
		_ = matched[i].UnmarshalSettings()
	}

	// 3. Sort policies from lowest precedence to highest precedence so that higher precedence overwrites
	// Precedence order: target_type rank (all: 1 < group: 2 < device: 3)
	// Within same target_type: priority DESC (e.g. 200 before 100), then created_at ASC
	// This ensures when iterating forward, the highest rank and lowest priority number (e.g. 10 > 100) overwrites!
	scopeRank := func(targetType string) int {
		switch targetType {
		case "device":
			return 3
		case "group":
			return 2
		default:
			return 1 // "all"
		}
	}

	sort.Slice(matched, func(i, j int) bool {
		ri, rj := scopeRank(matched[i].TargetType), scopeRank(matched[j].TargetType)
		if ri != rj {
			return ri < rj // lower rank first
		}
		if matched[i].Priority != matched[j].Priority {
			return matched[i].Priority > matched[j].Priority // higher priority number (lower precedence) first
		}
		return matched[i].CreatedAt.Before(matched[j].CreatedAt)
	})

	// Default baseline settings
	effective := PolicySettings{
		USBStorageMode:           USBStorageAllow,
		WallpaperEnabled:         false,
		WallpaperStyle:           WallpaperFill,
		InactivityTimeoutMinutes: 0,
		DisableTaskMgr:           false,
		DisableCMD:               false,
		DisableRegistryTools:     false,
		DisableControlPanel:      false,
		WindowsUpdateMode:        WindowsUpdateDefault,
	}

	// If no matched policies, version hash of empty baseline
	if len(matched) == 0 {
		return &CompiledPolicy{
			DeviceID: deviceID,
			Version:  ComputePolicyVersion(effective, r.versionKey),
			Settings: effective,
		}, nil
	}

	// Iterate and merge forward: higher precedence will override
	for _, p := range matched {
		s := p.Settings
		// USB Storage
		if s.USBStorageMode != "" {
			effective.USBStorageMode = s.USBStorageMode
		}
		// Wallpaper
		if s.WallpaperEnabled {
			effective.WallpaperEnabled = true
			effective.WallpaperURL = s.WallpaperURL
			effective.WallpaperStyle = s.WallpaperStyle
			effective.WallpaperLocked = s.WallpaperLocked
		} else if p.TargetType == "device" {
			// Explicit device policy can turn off wallpaper
			effective.WallpaperEnabled = false
			effective.WallpaperURL = ""
			effective.WallpaperLocked = false
		}
		// Inactivity Timeout
		if s.InactivityTimeoutMinutes > 0 || p.TargetType == "device" {
			effective.InactivityTimeoutMinutes = s.InactivityTimeoutMinutes
		}
		// Restrictions (cumulative across policies)
		if s.DisableTaskMgr {
			effective.DisableTaskMgr = true
		}
		if s.DisableCMD {
			effective.DisableCMD = true
		}
		if s.DisableRegistryTools {
			effective.DisableRegistryTools = true
		}
		if s.DisableControlPanel {
			effective.DisableControlPanel = true
		}
		if s.DisablePowerShell {
			effective.DisablePowerShell = true
		}
		if s.DisableAutoplay {
			effective.DisableAutoplay = true
		}
		if s.FirewallEnabled {
			effective.FirewallEnabled = true
		}
		if s.DefenderProtectionEnabled {
			effective.DefenderProtectionEnabled = true
		}
		if s.ScreenSaverTimeoutMinutes > 0 || p.TargetType == "device" {
			effective.ScreenSaverTimeoutMinutes = s.ScreenSaverTimeoutMinutes
		}
		if s.ScreenSaverPasswordRequired {
			effective.ScreenSaverPasswordRequired = true
		}

		// Windows Update
		if s.WindowsUpdateMode != "" && s.WindowsUpdateMode != WindowsUpdateDefault {
			effective.WindowsUpdateMode = s.WindowsUpdateMode
		}

		// Managed Local Admin Account
		if s.AdminAccountEnabled && s.AdminAccountUsername != "" && s.AdminAccountPassword != "" {
			effective.AdminAccountEnabled = true
			effective.AdminAccountUsername = s.AdminAccountUsername
			effective.AdminAccountPassword = s.AdminAccountPassword
			effective.AdminAccountFullName = s.AdminAccountFullName
			effective.AdminAccountDescription = s.AdminAccountDescription
			effective.AdminAccountNeverExpires = s.AdminAccountNeverExpires
		} else if p.TargetType == "device" && !s.AdminAccountEnabled {
			// Explicit device policy can turn off managed admin account
			effective.AdminAccountEnabled = false
			effective.AdminAccountUsername = ""
			effective.AdminAccountPassword = ""
			effective.AdminAccountFullName = ""
			effective.AdminAccountDescription = ""
			effective.AdminAccountNeverExpires = false
		}
	}

	version := ComputePolicyVersion(effective, r.versionKey)

	return &CompiledPolicy{
		DeviceID: deviceID,
		Version:  version,
		Settings: effective,
	}, nil
}

// ComputePolicyVersion generates a deterministic SHA-256 hash (16 chars) for
// settings. The managed admin password enters it only as an HMAC under key:
// it still changes the version when it changes, but a version no longer lets
// anyone who can read it check password guesses offline.
func ComputePolicyVersion(s PolicySettings, key []byte) string {
	if s.AdminAccountPassword != "" {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(s.AdminAccountPassword))
		s.AdminAccountPassword = hex.EncodeToString(m.Sum(nil))
	}
	s.AdminAccountPasswordSet = false
	raw, _ := json.Marshal(s)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:8]) // 16 hex chars
}

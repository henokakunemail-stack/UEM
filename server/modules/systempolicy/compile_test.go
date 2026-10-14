package systempolicy

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

func setupTestDB(t *testing.T) *Repository {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	return NewRepository(database)
}

func TestCompileEffectivePolicy_Precedence(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()

	// Create test device & group
	_, err := repo.db.Exec(`INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status, last_seen_at, enrolled_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-1', 'PC-FINANCE', 'Windows', '11 Pro', '1.4.0', 'online', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'secret-hash-1', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatalf("insert device: %v", err)
	}

	_, err = repo.db.Exec(`INSERT INTO device_groups (id, name, created_at, updated_at)
		VALUES ('grp-finance', 'Finance Dept', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatalf("insert group: %v", err)
	}

	_, err = repo.db.Exec(`INSERT INTO device_group_members (group_id, device_id, added_at, added_by)
		VALUES ('grp-finance', 'dev-1', CURRENT_TIMESTAMP, 'admin')`)
	if err != nil {
		t.Fatalf("insert group member: %v", err)
	}

	// 1. Fleet-wide baseline: allow USB, no wallpaper, timeout 30 min
	pFleet := &SystemPolicy{
		Name:       "Fleet Baseline",
		TargetType: "all",
		IsEnabled:  true,
		Priority:   100,
		CreatedBy:  "admin",
		Settings: PolicySettings{
			USBStorageMode:           USBStorageAllow,
			InactivityTimeoutMinutes: 30,
			DisableTaskMgr:           false,
		},
	}
	if err := repo.Create(ctx, pFleet); err != nil {
		t.Fatalf("create fleet policy: %v", err)
	}

	compiled, err := repo.CompileEffectivePolicy(ctx, "dev-1")
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}
	if compiled.Settings.USBStorageMode != USBStorageAllow {
		t.Errorf("expected USBStorageAllow, got %s", compiled.Settings.USBStorageMode)
	}
	if compiled.Settings.InactivityTimeoutMinutes != 30 {
		t.Errorf("expected InactivityTimeoutMinutes 30, got %d", compiled.Settings.InactivityTimeoutMinutes)
	}

	// 2. Group policy: block USB storage, timeout 15 min, disable task manager
	pGroup := &SystemPolicy{
		Name:       "Finance Strict Group",
		TargetType: "group",
		TargetID:   "grp-finance",
		IsEnabled:  true,
		Priority:   50,
		CreatedBy:  "admin",
		Settings: PolicySettings{
			USBStorageMode:           USBStorageBlock,
			InactivityTimeoutMinutes: 15,
			DisableTaskMgr:           true,
		},
	}
	if err := repo.Create(ctx, pGroup); err != nil {
		t.Fatalf("create group policy: %v", err)
	}

	compiledGroup, err := repo.CompileEffectivePolicy(ctx, "dev-1")
	if err != nil {
		t.Fatalf("compile policy after group: %v", err)
	}
	if compiledGroup.Settings.USBStorageMode != USBStorageBlock {
		t.Errorf("expected group policy USBStorageBlock, got %s", compiledGroup.Settings.USBStorageMode)
	}
	if compiledGroup.Settings.InactivityTimeoutMinutes != 15 {
		t.Errorf("expected group policy timeout 15, got %d", compiledGroup.Settings.InactivityTimeoutMinutes)
	}
	if !compiledGroup.Settings.DisableTaskMgr {
		t.Errorf("expected DisableTaskMgr true from group")
	}

	// 3. Device-specific policy: override USB to ReadOnly, wallpaper enabled
	pDevice := &SystemPolicy{
		Name:       "Executive Device Override",
		TargetType: "device",
		TargetID:   "dev-1",
		IsEnabled:  true,
		Priority:   10,
		CreatedBy:  "admin",
		Settings: PolicySettings{
			USBStorageMode:   USBStorageReadOnly,
			WallpaperEnabled: true,
			WallpaperURL:     "http://corp/wallpaper.jpg",
			WallpaperStyle:   WallpaperStretch,
		},
	}
	if err := repo.Create(ctx, pDevice); err != nil {
		t.Fatalf("create device policy: %v", err)
	}

	compiledDevice, err := repo.CompileEffectivePolicy(ctx, "dev-1")
	if err != nil {
		t.Fatalf("compile policy after device override: %v", err)
	}
	// Device scope must override group scope
	if compiledDevice.Settings.USBStorageMode != USBStorageReadOnly {
		t.Errorf("expected device policy USBStorageReadOnly, got %s", compiledDevice.Settings.USBStorageMode)
	}
	if !compiledDevice.Settings.WallpaperEnabled || compiledDevice.Settings.WallpaperURL != "http://corp/wallpaper.jpg" {
		t.Errorf("expected wallpaper enabled with URL, got %+v", compiledDevice.Settings)
	}
	// Inherited group settings should still apply if not overridden by device
	if !compiledDevice.Settings.DisableTaskMgr {
		t.Errorf("expected inherited DisableTaskMgr from group policy")
	}

	// 4. Test version determinism
	v1 := ComputePolicyVersion(compiledDevice.Settings)
	v2 := ComputePolicyVersion(compiledDevice.Settings)
	if v1 != v2 {
		t.Errorf("version hash not deterministic: %s != %s", v1, v2)
	}
	if len(v1) != 16 {
		t.Errorf("expected 16 hex chars version, got %d chars: %s", len(v1), v1)
	}
}

func TestDevicePolicyState_CRUD(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()

	_, err := repo.db.Exec(`INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status, last_seen_at, enrolled_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-state-1', 'DESKTOP-ABC', 'Windows', '10 Pro', '1.4.0', 'online', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'secret-hash-2', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatalf("insert device: %v", err)
	}

	initialState := &DeviceSystemPolicyState{
		DeviceID:      "dev-state-1",
		PolicyVersion: "a1b2c3d4e5f60718",
		Status:        "pending",
		AppliedSettings: PolicySettings{
			USBStorageMode: USBStorageBlock,
		},
		LastAppliedAt: time.Now().UTC(),
	}

	if err := repo.UpdateDeviceState(ctx, initialState); err != nil {
		t.Fatalf("insert state: %v", err)
	}

	got, err := repo.GetDeviceState(ctx, "dev-state-1")
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	if got == nil || got.Status != "pending" || got.PolicyVersion != "a1b2c3d4e5f60718" {
		t.Fatalf("unexpected state retrieved: %+v", got)
	}
	if got.AppliedSettings.USBStorageMode != USBStorageBlock {
		t.Errorf("expected USBStorageBlock in state, got %s", got.AppliedSettings.USBStorageMode)
	}

	// Update to synced
	got.Status = "synced"
	if err := repo.UpdateDeviceState(ctx, got); err != nil {
		t.Fatalf("update state: %v", err)
	}

	updated, err := repo.GetDeviceState(ctx, "dev-state-1")
	if err != nil {
		t.Fatalf("get updated state: %v", err)
	}
	if updated.Status != "synced" {
		t.Errorf("expected synced status, got %s", updated.Status)
	}
}

func TestCompileEffectivePolicy_NewSecurityPolicies(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()

	_, err := repo.db.Exec(`INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status, last_seen_at, enrolled_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-sec-1', 'PC-SECURE', 'Windows', '11 Pro', '1.4.0', 'online', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'secret-hash-sec', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatalf("insert device: %v", err)
	}

	p := &SystemPolicy{
		Name:       "Enterprise Security Hardening",
		TargetType: "device",
		TargetID:   "dev-sec-1",
		IsEnabled:  true,
		Priority:   1,
		CreatedBy:  "sec-admin",
		Settings: PolicySettings{
			WallpaperEnabled:            true,
			WallpaperURL:                "/api/system-policies/wallpapers/wallpaper_abc.png",
			WallpaperStyle:              WallpaperFill,
			WallpaperLocked:             true,
			FirewallEnabled:             true,
			DefenderProtectionEnabled:   true,
			DisablePowerShell:           true,
			DisableAutoplay:             true,
			ScreenSaverTimeoutMinutes:   10,
			ScreenSaverPasswordRequired: true,
		},
	}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("create security policy: %v", err)
	}

	compiled, err := repo.CompileEffectivePolicy(ctx, "dev-sec-1")
	if err != nil {
		t.Fatalf("compile policy: %v", err)
	}

	s := compiled.Settings
	if !s.WallpaperEnabled || !s.WallpaperLocked || s.WallpaperURL != "/api/system-policies/wallpapers/wallpaper_abc.png" {
		t.Errorf("wallpaper settings not compiled: %+v", s)
	}
	if !s.FirewallEnabled {
		t.Errorf("expected FirewallEnabled true")
	}
	if !s.DefenderProtectionEnabled {
		t.Errorf("expected DefenderProtectionEnabled true")
	}
	if !s.DisablePowerShell {
		t.Errorf("expected DisablePowerShell true")
	}
	if !s.DisableAutoplay {
		t.Errorf("expected DisableAutoplay true")
	}
	if s.ScreenSaverTimeoutMinutes != 10 {
		t.Errorf("expected ScreenSaverTimeoutMinutes 10, got %d", s.ScreenSaverTimeoutMinutes)
	}
	if !s.ScreenSaverPasswordRequired {
		t.Errorf("expected ScreenSaverPasswordRequired true")
	}
}

package systempolicy

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

// legacy builds a policy the way pre-tri-state fixtures were written.
func legacy(s PolicySettings) PolicyConfig { return fromLegacy(s) }

func (e *policyEnv) create(body map[string]any) string {
	e.t.Helper()
	code, out := e.do(http.MethodPost, "/api/system-policies", body)
	if code != http.StatusCreated {
		e.t.Fatalf("create %v: %d %s", body["name"], code, out)
	}
	var p SystemPolicy
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		e.t.Fatal(err)
	}
	return p.ID
}

func (e *policyEnv) compile() (*CompiledPolicy, map[string]SettingSource) {
	e.t.Helper()
	c, src, _, err := e.repo.CompileEffectivePolicyWithSources(context.Background(), "dev-1")
	if err != nil {
		e.t.Fatal(err)
	}
	return c, src
}

// TestLegacyBlobDecodesDefaultsAsNotSet: stored policies from before this change
// carry "allow", false and 0 for every field the operator never touched.
func TestLegacyBlobDecodesDefaultsAsNotSet(t *testing.T) {
	raw := `{"usb_storage_mode":"allow","wallpaper_enabled":false,"wallpaper_style":"fill",
		"disable_task_mgr":false,"disable_cmd":true,"inactivity_timeout_minutes":0,
		"screensaver_timeout_minutes":15,"windows_update_mode":"default","admin_account_enabled":false}`
	c, err := decodePolicyConfig([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if c.USBStorageMode != nil || c.WallpaperEnabled != nil || c.DisableTaskMgr != nil ||
		c.InactivityTimeoutMinutes != nil || c.WindowsUpdateMode != nil || c.AdminAccountEnabled != nil {
		t.Errorf("auto-filled legacy defaults decoded as decisions: %+v", c)
	}
	if c.DisableCMD == nil || !*c.DisableCMD || c.ScreenSaverTimeoutMinutes == nil || *c.ScreenSaverTimeoutMinutes != 15 {
		t.Errorf("legacy settings that turned something on were lost: %+v", c)
	}
	block, err := decodePolicyConfig([]byte(`{"usb_storage_mode":"block"}`))
	if err != nil || block.USBStorageMode == nil || *block.USBStorageMode != USBStorageBlock {
		t.Errorf("a legacy USB block did not survive: %+v %v", block, err)
	}
}

// TestUnsetUSBDoesNotLiftLowerBlock is the reported defect: a fleet policy
// blocks USB, and a device policy written only to set a wallpaper lifted the
// block, because every saved policy carried usb_storage_mode "allow".
func TestUnsetUSBDoesNotLiftLowerBlock(t *testing.T) {
	e := newPolicyEnv(t)
	e.create(map[string]any{"name": "Fleet USB block", "target_type": "all",
		"settings": map[string]any{"settings_version": 2, "usb_storage_mode": "block"}})
	// The current console: tri-state, USB left unset.
	e.create(map[string]any{"name": "Kiosk wallpaper", "target_type": "device", "target_id": "dev-1",
		"settings": map[string]any{"settings_version": 2, "wallpaper_enabled": true, "wallpaper_url": "https://corp/w.jpg"}})
	// A console tab opened before the upgrade: the old shape, defaults filled in.
	e.create(map[string]any{"name": "Old tab", "target_type": "device", "target_id": "dev-1", "priority": 5,
		"settings": map[string]any{"usb_storage_mode": "allow", "disable_task_mgr": false, "wallpaper_style": "fill"}})

	c, src := e.compile()
	if c.Settings.USBStorageMode != USBStorageBlock {
		t.Fatalf("USB = %q, want block: a policy that never decided USB lifted the fleet block", c.Settings.USBStorageMode)
	}
	if src[protocol.SettingUSBStorageMode].PolicyName != "Fleet USB block" {
		t.Errorf("USB decided by %+v, want the fleet policy", src[protocol.SettingUSBStorageMode])
	}
	if !c.Settings.WallpaperEnabled {
		t.Error("the wallpaper policy's own decision was lost")
	}
}

// TestExplicitAllowOverridesLowerBlock: an exception is still possible, on
// purpose. Restrictions used to be OR-merged, so a device could never be
// exempted from a broader Task Manager block.
func TestExplicitAllowOverridesLowerBlock(t *testing.T) {
	e := newPolicyEnv(t)
	e.create(map[string]any{"name": "Fleet lockdown", "target_type": "all",
		"settings": map[string]any{"settings_version": 2, "usb_storage_mode": "block", "disable_task_mgr": true, "disable_cmd": true}})
	e.create(map[string]any{"name": "IT workstation", "target_type": "device", "target_id": "dev-1",
		"settings": map[string]any{"settings_version": 2, "usb_storage_mode": "allow", "disable_task_mgr": false}})

	c, src := e.compile()
	if c.Settings.USBStorageMode != USBStorageAllow || c.Settings.DisableTaskMgr {
		t.Errorf("settings = usb %q taskmgr %v, want the device's explicit allow on both",
			c.Settings.USBStorageMode, c.Settings.DisableTaskMgr)
	}
	if !c.Settings.DisableCMD {
		t.Error("the fleet CMD block was dropped although the device policy never decided CMD")
	}
	for key, want := range map[string]string{
		protocol.SettingUSBStorageMode: "IT workstation",
		protocol.SettingDisableTaskMgr: "IT workstation",
		protocol.SettingDisableCMD:     "Fleet lockdown",
	} {
		if got := src[key].PolicyName; got != want {
			t.Errorf("%s decided by %q, want %q", key, got, want)
		}
	}
	if _, ok := src[protocol.SettingFirewallEnabled]; ok {
		t.Error("a setting nobody decided has a source")
	}
}

// TestEqualPriorityTieBreak: two group policies at the same priority and the
// same creation time must not resolve by whatever order the rows came back in.
func TestEqualPriorityTieBreak(t *testing.T) {
	e := newPolicyEnv(t)
	e.repo.db.MustExec(`INSERT INTO device_groups (id, name, created_at, updated_at) VALUES ('g1','A',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),('g2','B',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	e.repo.db.MustExec(`INSERT INTO device_group_members (group_id, device_id, added_at, added_by) VALUES ('g1','dev-1',CURRENT_TIMESTAMP,'a'),('g2','dev-1',CURRENT_TIMESTAMP,'a')`)
	a := e.create(map[string]any{"name": "A", "target_type": "group", "target_id": "g1",
		"settings": map[string]any{"settings_version": 2, "usb_storage_mode": "block"}})
	b := e.create(map[string]any{"name": "B", "target_type": "group", "target_id": "g2",
		"settings": map[string]any{"settings_version": 2, "usb_storage_mode": "read_only"}})
	e.repo.db.MustExec(`UPDATE system_policies SET created_at = '2026-01-01 00:00:00'`)

	want := "A"
	if b > a {
		want = "B"
	}
	for i := 0; i < 5; i++ {
		_, src := e.compile()
		if got := src[protocol.SettingUSBStorageMode].PolicyName; got != want {
			t.Fatalf("tie decided by %q, want %q (the larger id)", got, want)
		}
	}
}

func TestDevicePolicyEndpointIncludesSources(t *testing.T) {
	e := newPolicyEnv(t)
	e.create(map[string]any{"name": "Fleet USB block", "target_type": "all",
		"settings": map[string]any{"settings_version": 2, "usb_storage_mode": "block"}})
	code, body := e.do(http.MethodGet, "/api/devices/dev-1/system-policy", nil)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var resp struct {
		Sources map[string]SettingSource `json:"sources"`
		Matched []PolicyRef              `json:"matched_policies"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Sources[protocol.SettingUSBStorageMode].PolicyName != "Fleet USB block" || len(resp.Matched) != 1 {
		t.Errorf("response does not say who decided USB: %s", body)
	}
}

// TestLegacyRequestBodyConverted: an old console tab posts every field filled in.
func TestLegacyRequestBodyConverted(t *testing.T) {
	e := newPolicyEnv(t)
	id := e.create(map[string]any{"name": "Old tab", "target_type": "all",
		"settings": map[string]any{"usb_storage_mode": "allow", "disable_task_mgr": false, "firewall_enabled": true}})
	p, err := e.repo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Settings.USBStorageMode != nil || p.Settings.DisableTaskMgr != nil {
		t.Errorf("auto-filled defaults stored as decisions: %+v", p.Settings)
	}
	if p.Settings.FirewallEnabled == nil || !*p.Settings.FirewallEnabled {
		t.Error("the one real decision in the legacy body was lost")
	}
	if !strings.Contains(p.SettingsJSON, `"settings_version":2`) {
		t.Errorf("stored without the tri-state marker: %s", p.SettingsJSON)
	}
}

// TestCompiledEnvelopeShapeUnchanged: agents already in the field decode the
// payload into plain fields, so the compiled settings keep their shape.
func TestCompiledEnvelopeShapeUnchanged(t *testing.T) {
	e := newPolicyEnv(t)
	e.create(map[string]any{"name": "Fleet", "target_type": "all",
		"settings": map[string]any{"settings_version": 2, "usb_storage_mode": "block"}})
	c, _ := e.compile()
	raw, err := systemPolicyApplyEnvelope(c)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Payload struct {
			Settings map[string]any `json:"settings"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	for k, v := range env.Payload.Settings {
		if v == nil {
			t.Errorf("payload setting %s is null; an old agent cannot read it", k)
		}
	}
	if _, ok := env.Payload.Settings["settings_version"]; ok {
		t.Error("the tri-state marker leaked into the agent payload")
	}
	for _, k := range []string{"usb_storage_mode", "disable_task_mgr", "windows_update_mode", "inactivity_timeout_minutes"} {
		if _, ok := env.Payload.Settings[k]; !ok {
			t.Errorf("payload lost %s", k)
		}
	}
	if env.Payload.Settings["usb_storage_mode"] != "block" {
		t.Errorf("usb_storage_mode = %v", env.Payload.Settings["usb_storage_mode"])
	}
}

// TestSettingKeysMatchConfigTags keeps protocol.SettingKeys and the decision
// fields of PolicyConfig in step: a decision without a key has no source and
// no per-setting result.
func TestSettingKeysMatchConfigTags(t *testing.T) {
	var tags []string
	rt := reflect.TypeOf(PolicyConfig{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() == reflect.Pointer {
			tags = append(tags, strings.Split(f.Tag.Get("json"), ",")[0])
		}
	}
	keys := slices.Clone(protocol.SettingKeys)
	sort.Strings(tags)
	sort.Strings(keys)
	if !slices.Equal(tags, keys) {
		t.Errorf("decision fields %v != protocol.SettingKeys %v", tags, keys)
	}
}

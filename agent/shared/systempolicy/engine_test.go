package systempolicy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPolicySettings_JSON(t *testing.T) {
	s := PolicySettings{
		USBStorageMode:           USBStorageBlock,
		WallpaperEnabled:         true,
		WallpaperURL:             "https://corp.example.com/wp.jpg",
		WallpaperStyle:           WallpaperFill,
		InactivityTimeoutMinutes: 15,
		DisableTaskMgr:           true,
		DisableCMD:               true,
		DisableRegistryTools:     true,
		DisableControlPanel:      true,
		WindowsUpdateMode:        WindowsUpdateAutoInstall,
	}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed PolicySettings
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if parsed.USBStorageMode != USBStorageBlock {
		t.Errorf("expected %s, got %s", USBStorageBlock, parsed.USBStorageMode)
	}
	if !parsed.WallpaperEnabled {
		t.Errorf("expected wallpaper enabled")
	}
	if parsed.InactivityTimeoutMinutes != 15 {
		t.Errorf("expected 15, got %d", parsed.InactivityTimeoutMinutes)
	}
	if !parsed.DisableTaskMgr || !parsed.DisableCMD || !parsed.DisableRegistryTools || !parsed.DisableControlPanel {
		t.Errorf("expected restrictions enabled")
	}
	if parsed.WindowsUpdateMode != WindowsUpdateAutoInstall {
		t.Errorf("expected %s, got %s", WindowsUpdateAutoInstall, parsed.WindowsUpdateMode)
	}
}

func TestEngine_Report(t *testing.T) {
	reported := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/devices/dev-123/system-policy/report" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("X-Device-Id") != "dev-123" {
			t.Errorf("missing or invalid X-Device-Id")
		}
		if r.Header.Get("X-Device-Secret") != "sec-456" {
			t.Errorf("missing or invalid X-Device-Secret")
		}

		var payload struct {
			PolicyVersion string         `json:"policy_version"`
			Status        string         `json:"status"`
			Settings      PolicySettings `json:"settings"`
			ErrorMessage  string         `json:"error_message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode error: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if payload.PolicyVersion != "ver-abc" || payload.Status != "synced" {
			t.Errorf("unexpected payload: %+v", payload)
		}
		if payload.Settings.USBStorageMode != USBStorageBlock {
			t.Errorf("unexpected usb mode: %s", payload.Settings.USBStorageMode)
		}
		if payload.Settings.AdminAccountPassword != "" {
			t.Errorf("the report carried the managed admin password back to the server")
		}
		if payload.Settings.AdminAccountUsername != "sysadmin" {
			t.Errorf("the other admin-account fields must survive: username %q", payload.Settings.AdminAccountUsername)
		}

		reported = true
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	engine := NewEngine(ts.URL, "dev-123", "sec-456")
	s := PolicySettings{
		USBStorageMode:       USBStorageBlock,
		AdminAccountEnabled:  true,
		AdminAccountUsername: "sysadmin",
		AdminAccountPassword: "S3cret-For-Endpoints!",
	}
	err := engine.Report(context.Background(), "ver-abc", "synced", s, "")
	if err != nil {
		t.Fatalf("engine report failed: %v", err)
	}

	if !reported {
		t.Errorf("expected report handler to be called")
	}
}

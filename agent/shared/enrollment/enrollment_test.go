package enrollment

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestExchange_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/enroll" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			EnrollmentToken string `json:"enrollment_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.EnrollmentToken != "valid-test-token" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_id":     "dev-test-123",
			"device_secret": "sec-test-xyz",
		})
	}))
	defer server.Close()

	creds, err := Exchange(server.URL, "valid-test-token")
	if err != nil {
		t.Fatalf("Exchange failed: %v", err)
	}
	if creds.DeviceID != "dev-test-123" {
		t.Errorf("expected DeviceID=dev-test-123, got %s", creds.DeviceID)
	}
	if creds.DeviceSecret != "sec-test-xyz" {
		t.Errorf("expected DeviceSecret=sec-test-xyz, got %s", creds.DeviceSecret)
	}
	if creds.ServerURL != server.URL {
		t.Errorf("expected ServerURL=%s, got %s", server.URL, creds.ServerURL)
	}
}

func TestExchange_InvalidToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := Exchange(server.URL, "bad-token")
	if err == nil {
		t.Fatal("expected error for bad token, got nil")
	}
}

func TestLoadSave(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "creds.json")

	_, err := Load(p)
	if !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("expected ErrNotEnrolled, got %v", err)
	}

	testCreds := Credentials{
		DeviceID:     "dev-abc",
		DeviceSecret: "sec-123",
		ServerURL:    "https://mgmt.example.com",
	}
	if err := Save(p, testCreds); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load(p)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.DeviceID != testCreds.DeviceID || loaded.DeviceSecret != testCreds.DeviceSecret {
		t.Errorf("mismatch: got %+v, want %+v", loaded, testCreds)
	}

	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if info.Size() == 0 {
		t.Errorf("expected non-empty file")
	}
}

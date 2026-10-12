package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/enrollment"
)

func TestRunEnrollOnly_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/enroll" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			EnrollmentToken string `json:"enrollment_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.EnrollmentToken != "test-token-abc" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_id":     "dev-enroll-only-1",
			"device_secret": "secret-enroll-only-1",
		})
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	credsPath := filepath.Join(tmpDir, "creds.json")

	err := runEnrollOnly(server.URL, "test-token-abc", credsPath)
	if err != nil {
		t.Fatalf("runEnrollOnly failed: %v", err)
	}

	creds, err := enrollment.Load(credsPath)
	if err != nil {
		t.Fatalf("failed to load saved creds: %v", err)
	}
	if creds.DeviceID != "dev-enroll-only-1" {
		t.Errorf("expected DeviceID=dev-enroll-only-1, got %s", creds.DeviceID)
	}
	if creds.DeviceSecret != "secret-enroll-only-1" {
		t.Errorf("expected DeviceSecret=secret-enroll-only-1, got %s", creds.DeviceSecret)
	}
	if creds.ServerURL != server.URL {
		t.Errorf("expected ServerURL=%s, got %s", server.URL, creds.ServerURL)
	}
}

func TestRunEnrollOnly_MissingToken(t *testing.T) {
	tmpDir := t.TempDir()
	credsPath := filepath.Join(tmpDir, "creds.json")

	err := runEnrollOnly("http://localhost:8443", "", credsPath)
	if err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
}

func TestRunEnrollOnly_MissingServer(t *testing.T) {
	tmpDir := t.TempDir()
	credsPath := filepath.Join(tmpDir, "creds.json")

	err := runEnrollOnly("", "some-token", credsPath)
	if err == nil {
		t.Fatal("expected error for empty server URL, got nil")
	}
}

func TestRunEnrollOnly_InvalidToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	credsPath := filepath.Join(tmpDir, "creds.json")

	err := runEnrollOnly(server.URL, "bad-token", credsPath)
	if err == nil {
		t.Fatal("expected error for bad token, got nil")
	}
}

func TestRunEnrollOnly_DeviceIDMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_id":     "dev-new-id",
			"device_secret": "secret-new",
		})
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	credsPath := filepath.Join(tmpDir, "creds.json")

	// Pre-populate with different device ID
	existingCreds := enrollment.Credentials{
		DeviceID:     "dev-old-id",
		DeviceSecret: "secret-old",
		ServerURL:    server.URL,
	}
	if err := enrollment.Save(credsPath, existingCreds); err != nil {
		t.Fatalf("failed to pre-populate creds: %v", err)
	}

	err := runEnrollOnly(server.URL, "valid-token", credsPath)
	if err == nil {
		t.Fatal("expected error for device ID mismatch, got nil")
	}
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/enrollment"
)

func TestRunVerifyUninstall_Unenrolled(t *testing.T) {
	tmpDir := t.TempDir()
	credsPath := filepath.Join(tmpDir, "creds.json")

	// Missing creds file means unenrolled device, should exit with 0
	code := runVerifyUninstall("http://127.0.0.1:8080", credsPath, "some-token")
	if code != 0 {
		t.Errorf("expected exit code 0 for unenrolled device, got %d", code)
	}
}

func TestRunVerifyUninstall_ValidToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/devices/dev-test-123/verify-uninstall" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Device-Id") != "dev-test-123" || r.Header.Get("X-Device-Secret") != "secret-123" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Token != "UNINST-VALID-ABC" {
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]bool{"valid": true})
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	credsPath := filepath.Join(tmpDir, "creds.json")
	creds := enrollment.Credentials{
		DeviceID:     "dev-test-123",
		DeviceSecret: "secret-123",
		ServerURL:    server.URL,
	}
	if err := enrollment.Save(credsPath, creds); err != nil {
		t.Fatalf("failed to save creds: %v", err)
	}

	code := runVerifyUninstall(server.URL, credsPath, "UNINST-VALID-ABC")
	if code != 0 {
		t.Errorf("expected exit code 0 for valid token, got %d", code)
	}
}

func TestRunVerifyUninstall_InvalidToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid token"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	credsPath := filepath.Join(tmpDir, "creds.json")
	creds := enrollment.Credentials{
		DeviceID:     "dev-test-123",
		DeviceSecret: "secret-123",
		ServerURL:    server.URL,
	}
	if err := enrollment.Save(credsPath, creds); err != nil {
		t.Fatalf("failed to save creds: %v", err)
	}

	code := runVerifyUninstall(server.URL, credsPath, "UNINST-WRONG-TOKEN")
	if code != 1 {
		t.Errorf("expected exit code 1 for invalid token, got %d", code)
	}
}

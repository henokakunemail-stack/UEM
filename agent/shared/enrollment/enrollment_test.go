package enrollment

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

	creds, err := Exchange(server.URL, "valid-test-token", "")
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

func TestExchange_WithSite(t *testing.T) {
	var receivedSite string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			EnrollmentToken string `json:"enrollment_token"`
			Site            string `json:"site"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedSite = req.Site
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"device_id":     "dev-site-123",
			"device_secret": "sec-site-xyz",
		})
	}))
	defer server.Close()

	creds, err := Exchange(server.URL, "valid-token", "Medan-Branch")
	if err != nil {
		t.Fatalf("Exchange with site failed: %v", err)
	}
	if creds.DeviceID != "dev-site-123" {
		t.Errorf("expected DeviceID=dev-site-123, got %s", creds.DeviceID)
	}
	if receivedSite != "Medan-Branch" {
		t.Errorf("expected server to receive site=Medan-Branch, got %s", receivedSite)
	}
}

func TestExchange_InvalidToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := Exchange(server.URL, "bad-token", "")
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

// The installer and the operator need the server's reason, not a bare status
// code: "no uses left" and "expired" both used to read "HTTP 401".
func TestExchangeReportsTheServerReason(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotToken = body["enrollment_token"]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"enrollment token has no uses left","code":"token_used_up"}`))
	}))
	defer srv.Close()

	_, err := Exchange(srv.URL, "  batch-token\n", "")
	if err == nil || !strings.Contains(err.Error(), "token_used_up") || !strings.Contains(err.Error(), "no uses left") {
		t.Fatalf("err = %v, want the server's reason and code", err)
	}
	if gotToken != "batch-token" {
		t.Fatalf("token sent = %q, want it trimmed", gotToken)
	}
}

func identityServer(t *testing.T, status int, calls *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/identity" {
			*calls++
			if r.Header.Get("X-Device-Id") == "" || r.Header.Get("X-Device-Secret") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Running an installer again used to enroll every time. With a multi-device
// token each run made a duplicate device and spent one of the token's uses.
func TestShouldEnrollKeepsAnIdentityTheServerStillAccepts(t *testing.T) {
	calls := 0
	srv := identityServer(t, http.StatusOK, &calls)
	creds := Credentials{DeviceID: "d1", DeviceSecret: "s", ServerURL: srv.URL}

	if got, err := ShouldEnroll(srv.URL, creds, false); err != nil || got {
		t.Fatalf("valid identity: enroll=%v err=%v, want keep it", got, err)
	}
	if got, _ := ShouldEnroll(srv.URL, creds, true); !got {
		t.Fatal("-reenroll did not force a new enrollment")
	}
	if got, _ := ShouldEnroll(srv.URL, Credentials{}, false); !got {
		t.Fatal("no credentials: want enroll")
	}
	if got, _ := ShouldEnroll(srv.URL, Credentials{DeviceID: "d1", DeviceSecret: "s", ServerURL: "http://other:8443"}, false); !got {
		t.Fatal("credentials for another server: want enroll")
	}
	if calls != 1 {
		t.Fatalf("identity checks = %d, want 1 (only the case that needs the server)", calls)
	}
}

func TestShouldEnrollReplacesAnIdentityTheServerRefuses(t *testing.T) {
	calls := 0
	srv := identityServer(t, http.StatusUnauthorized, &calls)
	if got, err := ShouldEnroll(srv.URL, Credentials{DeviceID: "d1", DeviceSecret: "s"}, false); err != nil || !got {
		t.Fatalf("refused identity: enroll=%v err=%v, want enroll", got, err)
	}
	broken := identityServer(t, http.StatusInternalServerError, &calls)
	if _, err := ShouldEnroll(broken.URL, Credentials{DeviceID: "d1", DeviceSecret: "s"}, false); err == nil {
		t.Fatal("a server error was taken as an answer")
	}
}

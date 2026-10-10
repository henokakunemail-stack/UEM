// Package enrollment exchanges a one-time enrollment token for a persistent
// device secret, then persists it on disk so the agent survives restarts.
package enrollment

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/transport"
)

// Credentials is what the agent stores locally after a successful enrollment.
type Credentials struct {
	DeviceID     string `json:"device_id"`
	DeviceSecret string `json:"device_secret"`
	ServerURL    string `json:"server_url"`
}

// Exchange posts the enrollment token to the server and stores the returned secret.
func Exchange(serverURL, token string) (Credentials, error) {
	body, _ := json.Marshal(map[string]string{"enrollment_token": token})
	client := transport.NewHTTPClient(15 * time.Second)
	resp, err := client.Post(serverURL+"/api/agent/enroll", "application/json", bytesReader(body))
	if err != nil {
		return Credentials{}, fmt.Errorf("enroll request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Credentials{}, fmt.Errorf("enroll failed: HTTP %d", resp.StatusCode)
	}
	var out struct {
		DeviceID     string `json:"device_id"`
		DeviceSecret string `json:"device_secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Credentials{}, fmt.Errorf("decode enroll response: %w", err)
	}
	if out.DeviceID == "" || out.DeviceSecret == "" {
		return Credentials{}, errors.New("server returned empty credentials")
	}
	return Credentials{DeviceID: out.DeviceID, DeviceSecret: out.DeviceSecret, ServerURL: serverURL}, nil
}

// Load reads persisted credentials. Returns ErrNotEnrolled if absent.
func Load(path string) (Credentials, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, ErrNotEnrolled
	}
	if err != nil {
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return Credentials{}, err
	}
	return c, nil
}

// Save writes credentials with restrictive permissions.
func Save(path string, c Credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	// On Windows, tighten ACL to SYSTEM + Administrators only.
	// This is best-effort; failure is non-fatal because default perms are 0600.
	if runtime.GOOS == "windows" {
		restrictToSystemAndAdmins(path)
	}
	return nil
}

// restrictToSystemAndAdmins applies a restrictive DACL on Windows so that
// only SYSTEM, Administrators, and the executing user can read the credentials file.
// It is a no-op on non-Windows platforms.
func restrictToSystemAndAdmins(path string) {
	args := []string{path, "/inheritance:r", "/grant:r", "SYSTEM:F", "/grant:r", "BUILTIN\\Administrators:F"}
	if u, err := user.Current(); err == nil && u.Username != "" {
		args = append(args, "/grant:r", u.Username+":F")
	}
	_ = exec.Command("icacls", args...).Run()
}

// ErrNotEnrolled means the agent has not enrolled yet (no local credentials).
var ErrNotEnrolled = errors.New("agent not enrolled")

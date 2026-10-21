// Package enrollment exchanges a one-time enrollment token for a persistent
// device secret, then persists it on disk so the agent survives restarts.
package enrollment

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/transport"
)

// Credentials is what the agent stores locally after a successful enrollment.
type Credentials struct {
	DeviceID     string `json:"device_id"`
	DeviceSecret string `json:"device_secret"`
	ServerURL    string `json:"server_url"`
}

// Exchange posts the enrollment token to the server and stores the returned secret.
// If osinfo.Info is provided, it enriches the enrollment request with device facts.
// site optionally specifies the physical/logical branch location of the device.
func Exchange(serverURL, token, site string, info ...osinfo.Info) (Credentials, error) {
	normURL := transport.NormalizeServerURL(serverURL)
	payload := map[string]string{
		// A token pasted with a trailing space or newline is still the token.
		"enrollment_token": strings.TrimSpace(token),
	}
	if s := strings.TrimSpace(site); s != "" {
		payload["site"] = s
	}
	if len(info) > 0 {
		if info[0].Hostname != "" {
			payload["hostname"] = info[0].Hostname
		}
		if info[0].Name != "" {
			payload["os_name"] = info[0].Name
		}
		if info[0].Version != "" {
			payload["os_version"] = info[0].Version
		}
		if info[0].AgentVersion != "" {
			payload["agent_version"] = info[0].AgentVersion
		}
	}
	body, _ := json.Marshal(payload)
	client := transport.NewHTTPClient(15 * time.Second)
	resp, err := client.Post(normURL+"/api/agent/enroll", "application/json", bytesReader(body))
	if err != nil {
		return Credentials{}, fmt.Errorf("enroll request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Credentials{}, enrollError(resp)
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
	return Credentials{DeviceID: out.DeviceID, DeviceSecret: out.DeviceSecret, ServerURL: normURL}, nil
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

// enrollError says why the server refused. It used to report only the status
// code, so a batch token with no uses left, an expired token and a rate limit
// all read as "HTTP 401" or "HTTP 429" and the installer could only guess.
func enrollError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if json.Unmarshal(raw, &body) == nil && body.Error != "" {
		if body.Code != "" {
			return fmt.Errorf("enroll failed: HTTP %d: %s (%s)", resp.StatusCode, body.Error, body.Code)
		}
		return fmt.Errorf("enroll failed: HTTP %d: %s", resp.StatusCode, body.Error)
	}
	if text := strings.TrimSpace(string(raw)); text != "" {
		return fmt.Errorf("enroll failed: HTTP %d: %s", resp.StatusCode, text)
	}
	return fmt.Errorf("enroll failed: HTTP %d", resp.StatusCode)
}

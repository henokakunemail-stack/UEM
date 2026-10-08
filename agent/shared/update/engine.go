package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/transport"
)

type UpdateParams struct {
	TaskID         string `json:"task_id"`
	TargetVersion  string `json:"target_version"`
	DownloadURL    string `json:"download_url"`
	SHA256Checksum string `json:"sha256_checksum"`
	FileSize       int64  `json:"file_size"`
	// Manifest carries the signed release statement. It is optional because a
	// fleet mid-migration still has unsigned releases queued, and a blank
	// signature is a policy decision for the caller rather than an install
	// failure -- see ApplyUpdate.
	Manifest *SignedManifest `json:"manifest,omitempty"`
}

type Engine struct {
	serverURL    string
	deviceID     string
	deviceSecret string
	client       *http.Client
	execPath     string
	// publicKey is the trusted release-signing key. Nil means the agent was not
	// built/configured with one, in which case signed verification is skipped
	// and only the checksum applies. That is a build-time decision, not a
	// runtime one: a production agent gets a key embedded at build.
	publicKey ed25519.PublicKey
	// currentVersion is what the running agent reports as its own version. It
	// is the input to the downgrade floor check, and it comes from the value
	// ldflags stamped into the binary rather than from anything the server
	// says, so a server that has been fed a bad version cannot talk an agent
	// down to an unsupported one.
	currentVersion string
	mu             sync.Mutex
}

func NewEngine(serverURL, deviceID, deviceSecret string) *Engine {
	return &Engine{
		serverURL:    serverURL,
		deviceID:     deviceID,
		deviceSecret: deviceSecret,
		client:       transport.NewHTTPClient(60 * time.Second),
	}
}

// WithSigningKey trusts the given public key for release manifests and sets
// the version the downgrade floor is measured against. Both are builder
// inputs; nothing here is learned from the server at runtime.
func (e *Engine) WithSigningKey(publicKey ed25519.PublicKey, currentVersion string) *Engine {
	e.publicKey = publicKey
	e.currentVersion = currentVersion
	return e
}

// SetExecutablePath allows overriding the binary target path for tests.
func (e *Engine) SetExecutablePath(p string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.execPath = p
}

func (e *Engine) getCurrentExecPath() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.execPath != "" {
		return e.execPath, nil
	}
	return os.Executable()
}

func (e *Engine) ApplyUpdate(ctx context.Context, params UpdateParams) error {
	// 1. Report downloading status
	_ = e.ReportProgress(ctx, params.TaskID, "downloading", params.TargetVersion, "")

	fullDownloadURL := params.DownloadURL
	if fullDownloadURL == "" {
		// A rollout row with no artifact URL would otherwise index an empty
		// string and panic, killing the whole agent (there is no recover() on
		// the update goroutine). Fail the task cleanly instead.
		err := errors.New("download_url is empty")
		_ = e.ReportProgress(ctx, params.TaskID, "failed", params.TargetVersion, err.Error())
		return err
	}
	if fullDownloadURL[0] == '/' {
		fullDownloadURL = e.serverURL + fullDownloadURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullDownloadURL, nil)
	if err != nil {
		_ = e.ReportProgress(ctx, params.TaskID, "failed", params.TargetVersion, err.Error())
		return fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("X-Device-Id", e.deviceID)
	req.Header.Set("X-Device-Secret", e.deviceSecret)

	resp, err := e.client.Do(req)
	if err != nil {
		_ = e.ReportProgress(ctx, params.TaskID, "failed", params.TargetVersion, err.Error())
		return fmt.Errorf("download binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("download failed with status %d", resp.StatusCode)
		_ = e.ReportProgress(ctx, params.TaskID, "failed", params.TargetVersion, err.Error())
		return err
	}

	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, fmt.Sprintf("emagent_update_%s.tmp", params.TaskID))
	f, err := os.OpenFile(tempFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		_ = e.ReportProgress(ctx, params.TaskID, "failed", params.TargetVersion, err.Error())
		return fmt.Errorf("create temp file: %w", err)
	}

	hasher := sha256.New()
	writer := io.MultiWriter(f, hasher)
	_, err = io.Copy(writer, resp.Body)
	_ = f.Close()
	if err != nil {
		_ = os.Remove(tempFile)
		_ = e.ReportProgress(ctx, params.TaskID, "failed", params.TargetVersion, err.Error())
		return fmt.Errorf("save download stream: %w", err)
	}

	// 2. Report verifying status
	_ = e.ReportProgress(ctx, params.TaskID, "verifying", params.TargetVersion, "")
	downloadedHash := hex.EncodeToString(hasher.Sum(nil))
	if downloadedHash != params.SHA256Checksum {
		_ = os.Remove(tempFile)
		errMsg := fmt.Sprintf("sha256 mismatch: expected %s, got %s", params.SHA256Checksum, downloadedHash)
		_ = e.ReportProgress(ctx, params.TaskID, "failed", params.TargetVersion, errMsg)
		return errors.New(errMsg)
	}

	// 2b. Verify the signed manifest. The checksum above proves the download
	// matches what the server recorded; the signature proves a key the agent
	// trusts saw it, which is a different and stronger claim -- it is what
	// stops a server that has been taken over from shipping a binary of its
	// own choosing, because the private key is not on the server.
	//
	// A fleet that is still mid-migration has releases queued with no
	// signature. Failing those outright would leave devices that were
	// scheduled to upgrade unable to, with the only recovery being a manual
	// re-upload, so an unsigned manifest is allowed only when the agent itself
	// was built without a trusted key. An agent that has a key requires a
	// signature, full stop.
	if params.Manifest != nil {
		if err := e.verifyManifest(*params.Manifest, params); err != nil {
			_ = os.Remove(tempFile)
			_ = e.ReportProgress(ctx, params.TaskID, "failed", params.TargetVersion, err.Error())
			return err
		}
	} else if e.publicKey != nil {
		_ = os.Remove(tempFile)
		err := errors.New("update has no signed manifest; this agent requires a signed release")
		_ = e.ReportProgress(ctx, params.TaskID, "failed", params.TargetVersion, err.Error())
		return err
	}

	// 2c. Refuse to move below the version the fleet no longer supports. This
	// is the floor, not a general downgrade prohibition: an operator may have
	// a reason to move a device back one version, but a release that declares
	// a minimum above where the device already is means going there breaks
	// something the fleet has already decided it will not tolerate.
	if params.Manifest != nil && params.Manifest.Manifest.MinimumSupportedVersion != "" {
		if belowMinimum(e.currentVersion, params.Manifest.Manifest.MinimumSupportedVersion) {
			_ = os.Remove(tempFile)
			err := fmt.Errorf("device version %s is below the release minimum supported version %s",
				e.currentVersion, params.Manifest.Manifest.MinimumSupportedVersion)
			_ = e.ReportProgress(ctx, params.TaskID, "failed", params.TargetVersion, err.Error())
			return err
		}
	}

	// 3. Report swapping status
	_ = e.ReportProgress(ctx, params.TaskID, "swapping", params.TargetVersion, "")
	currentExec, err := e.getCurrentExecPath()
	if err != nil {
		_ = os.Remove(tempFile)
		_ = e.ReportProgress(ctx, params.TaskID, "failed", params.TargetVersion, err.Error())
		return fmt.Errorf("resolve current executable: %w", err)
	}

	backupExec := currentExec + ".old"
	_ = os.Remove(backupExec) // remove any leftover old backup

	// Backup running binary: rename current -> current.old
	// On Windows, moving/renaming a running binary is permitted, whereas overwriting is not.
	if err := os.Rename(currentExec, backupExec); err != nil {
		_ = os.Remove(tempFile)
		_ = e.ReportProgress(ctx, params.TaskID, "failed", params.TargetVersion, fmt.Sprintf("backup rename failed: %v", err))
		return fmt.Errorf("backup binary: %w", err)
	}

	// Move new binary into place
	if err := os.Rename(tempFile, currentExec); err != nil {
		// Rollback immediately if rename fails
		_ = os.Rename(backupExec, currentExec)
		_ = os.Remove(tempFile)
		_ = e.ReportProgress(ctx, params.TaskID, "rollback", params.TargetVersion, fmt.Sprintf("swap failed, rolled back: %v", err))
		return fmt.Errorf("swap binary: %w", err)
	}
	_ = os.Chmod(currentExec, 0755)

	// 4. Health verification: the swapped-in file must be one the OS can load.
	//
	// This used to be os.Stat plus a non-zero size, which proves the rename
	// landed and something non-empty is there. A text file renamed to .exe
	// passes it, and so does any blob that happens to match the recorded
	// checksum. Both were reported to the server as success, which rewrote the
	// device's agent_version, so the fleet's inventory claimed the upgrade
	// happened while the binary on disk could not start -- and the device only
	// discovered that at its next restart, with the update as the last thing
	// that touched it.
	if err := verifySwappedBinary(currentExec); err != nil {
		// Rollback
		_ = os.Remove(currentExec)
		_ = os.Rename(backupExec, currentExec)
		msg := "healthcheck failed, rolled back: " + err.Error()
		_ = e.ReportProgress(ctx, params.TaskID, "rollback", params.TargetVersion, msg)
		return errors.New("post-swap healthcheck failed, rolled back to original binary: " + err.Error())
	}

	// 5. Report success
	_ = e.ReportProgress(ctx, params.TaskID, "success", params.TargetVersion, "")
	return nil
}

// verifyManifest checks a signed release statement against the key this agent
// was built to trust, and that it describes the artifact actually downloaded.
//
// The signature is the authenticity check; the field-by-field comparison is
// the freshness and binding check. Without it a correctly-signed manifest
// from an older release would verify cleanly against bytes that are not the
// ones it was issued for, so the manifest's own sha256 and size are compared
// to what the download produced, and its platform to the one the agent runs.
func (e *Engine) verifyManifest(sm SignedManifest, params UpdateParams) error {
	if e.publicKey == nil {
		return ErrNoSignature
	}
	if err := sm.Manifest.Verify(e.publicKey, sm.Signature); err != nil {
		return err
	}
	if sm.Manifest.SHA256Checksum != params.SHA256Checksum {
		return fmt.Errorf("manifest checksum does not match the downloaded artifact")
	}
	if params.FileSize > 0 && sm.Manifest.Size != params.FileSize {
		return fmt.Errorf("manifest size %d does not match the downloaded artifact %d",
			sm.Manifest.Size, params.FileSize)
	}
	if sm.Manifest.OSName != releaseOSName() || sm.Manifest.Arch != releaseArch() {
		return fmt.Errorf("manifest is for %s/%s, this agent runs %s/%s",
			sm.Manifest.OSName, sm.Manifest.Arch, releaseOSName(), releaseArch())
	}
	return nil
}

// releaseOSName maps runtime.GOOS to the os_name the console and the release
// table use. They differ for one platform (darwin vs macos) and every release
// in the table is stored under the console name, so a raw GOOS comparison
// would reject every macOS release as a platform mismatch.
func releaseOSName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	default:
		return runtime.GOOS
	}
}

// releaseArch maps runtime.GOARCH to the arch the release table stores. The
// server's two dispatch paths both hardcode "amd64" as the default arch, so
// GOARCH must be translated to match it rather than the other way around.
func releaseArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	case "386":
		return "386"
	default:
		return runtime.GOARCH
	}
}

func (e *Engine) ReportProgress(ctx context.Context, taskID, status, targetVersion, errMsg string) error {
	body := map[string]string{
		"task_id":        taskID,
		"status":         status,
		"target_version": targetVersion,
		"error_message":  errMsg,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/api/agent/devices/%s/update/report", e.serverURL, e.deviceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device-Id", e.deviceID)
	req.Header.Set("X-Device-Secret", e.deviceSecret)

	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("report progress failed with status %d", resp.StatusCode)
	}
	return nil
}

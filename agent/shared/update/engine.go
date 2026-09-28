package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
}

type Engine struct {
	serverURL    string
	deviceID     string
	deviceSecret string
	client       *http.Client
	execPath     string
	mu           sync.Mutex
}

func NewEngine(serverURL, deviceID, deviceSecret string) *Engine {
	return &Engine{
		serverURL:    serverURL,
		deviceID:     deviceID,
		deviceSecret: deviceSecret,
		client:       transport.NewHTTPClient(60 * time.Second),
	}
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

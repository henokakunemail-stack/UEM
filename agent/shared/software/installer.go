package software

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
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/transport"
)

type InstallPayload struct {
	TaskID      string `json:"task_id"`
	PackageID   string `json:"package_id"`
	PackageName string `json:"package_name"`
	DownloadURL string `json:"download_url"`
	FileName    string `json:"file_name"`
	PackageType string `json:"package_type"`
	SHA256      string `json:"sha256"`
	InstallArgs string `json:"install_args"`
}

type ProgressReport struct {
	TaskID       string  `json:"task_id"`
	Status       string  `json:"status"` // downloading, installing, success, failed
	ExitCode     *int    `json:"exit_code,omitempty"`
	OutputLog    *string `json:"output_log,omitempty"`
	ErrorMessage *string `json:"error_message,omitempty"`
}

type Runner interface {
	Run(ctx context.Context, filePath, packageType, installArgs string) (exitCode int, output string, err error)
}

// ToHTTPURL converts ws:// or wss:// server URLs to http:// or https://.
func ToHTTPURL(serverURL string) string {
	if strings.HasPrefix(serverURL, "wss://") {
		return "https://" + strings.TrimPrefix(serverURL, "wss://")
	}
	if strings.HasPrefix(serverURL, "ws://") {
		return "http://" + strings.TrimPrefix(serverURL, "ws://")
	}
	return strings.TrimRight(serverURL, "/")
}

// ReportProgress notifies the server about installation progress.
func ReportProgress(ctx context.Context, serverURL, deviceID, deviceSecret string, rep ProgressReport) error {
	apiBase := ToHTTPURL(serverURL)
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}

	reqURL := fmt.Sprintf("%s/api/agent/tasks/%s/progress", apiBase, rep.TaskID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device-Id", deviceID)
	req.Header.Set("X-Device-Secret", deviceSecret)

	client := transport.NewHTTPClient(15 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("server rejected progress report: status %d", resp.StatusCode)
	}
	return nil
}

// ExecuteInstall downloads, verifies, and runs a software package.
func ExecuteInstall(ctx context.Context, serverURL, deviceID, deviceSecret string, rawPayload json.RawMessage) error {
	var payload InstallPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}

	if payload.TaskID == "" || payload.DownloadURL == "" {
		return errors.New("invalid install payload: missing task_id or download_url")
	}

	log.Info().
		Str("task_id", payload.TaskID).
		Str("package_name", payload.PackageName).
		Str("file_name", payload.FileName).
		Msg("received software install job")

	// 1. Report downloading status
	_ = ReportProgress(ctx, serverURL, deviceID, deviceSecret, ProgressReport{
		TaskID: payload.TaskID,
		Status: "downloading",
	})

	// 2. Download package file
	apiBase := ToHTTPURL(serverURL)
	downloadURL := apiBase + payload.DownloadURL
	if strings.HasPrefix(payload.DownloadURL, "http://") || strings.HasPrefix(payload.DownloadURL, "https://") {
		downloadURL = payload.DownloadURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		failMsg := "failed to create download request: " + err.Error()
		_ = ReportProgress(ctx, serverURL, deviceID, deviceSecret, ProgressReport{
			TaskID:       payload.TaskID,
			Status:       "failed",
			ErrorMessage: &failMsg,
		})
		return err
	}
	req.Header.Set("X-Device-Id", deviceID)
	req.Header.Set("X-Device-Secret", deviceSecret)

	downloadClient := transport.NewHTTPClient(30 * time.Minute)
	resp, err := downloadClient.Do(req)
	if err != nil {
		failMsg := "download failed: " + err.Error()
		_ = ReportProgress(ctx, serverURL, deviceID, deviceSecret, ProgressReport{
			TaskID:       payload.TaskID,
			Status:       "failed",
			ErrorMessage: &failMsg,
		})
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		failMsg := fmt.Sprintf("download failed: http status %d", resp.StatusCode)
		_ = ReportProgress(ctx, serverURL, deviceID, deviceSecret, ProgressReport{
			TaskID:       payload.TaskID,
			Status:       "failed",
			ErrorMessage: &failMsg,
		})
		return errors.New(failMsg)
	}

	tempDir := os.TempDir()
	tempPath := filepath.Join(tempDir, fmt.Sprintf("epm_%s_%s", payload.TaskID, filepath.Base(payload.FileName)))

	destFile, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		failMsg := "failed to create temp file: " + err.Error()
		_ = ReportProgress(ctx, serverURL, deviceID, deviceSecret, ProgressReport{
			TaskID:       payload.TaskID,
			Status:       "failed",
			ErrorMessage: &failMsg,
		})
		return err
	}

	hasher := sha256.New()
	mw := io.MultiWriter(destFile, hasher)

	_, copyErr := io.Copy(mw, resp.Body)
	_ = destFile.Close()

	if copyErr != nil {
		_ = os.Remove(tempPath)
		failMsg := "failed to save download: " + copyErr.Error()
		_ = ReportProgress(ctx, serverURL, deviceID, deviceSecret, ProgressReport{
			TaskID:       payload.TaskID,
			Status:       "failed",
			ErrorMessage: &failMsg,
		})
		return copyErr
	}

	// 3. Verify SHA-256 Checksum
	actualSHA256 := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualSHA256, payload.SHA256) {
		_ = os.Remove(tempPath)
		failMsg := fmt.Sprintf("checksum mismatch: expected %s, got %s", payload.SHA256, actualSHA256)
		log.Error().Str("task_id", payload.TaskID).Msg(failMsg)
		_ = ReportProgress(ctx, serverURL, deviceID, deviceSecret, ProgressReport{
			TaskID:       payload.TaskID,
			Status:       "failed",
			ErrorMessage: &failMsg,
		})
		return errors.New(failMsg)
	}

	// 4. Report installing status
	_ = ReportProgress(ctx, serverURL, deviceID, deviceSecret, ProgressReport{
		TaskID: payload.TaskID,
		Status: "installing",
	})

	// 5. Execute installer
	//
	// The deadline covers the process only. The download above has its own
	// 30-minute HTTP client, and the two budgets are unrelated: a slow mirror
	// should not eat the installer's time, and a hung installer should not be
	// reported as a network problem.
	runCtx, cancelRun := taskContext(ctx)
	defer cancelRun()

	runner := DefaultRunner()
	exitCode, output, runErr := runner.Run(runCtx, tempPath, payload.PackageType, payload.InstallArgs)
	_ = os.Remove(tempPath) // Clean up temp file immediately

	isSuccess := false
	if runErr == nil {
		isSuccess = true
	} else if payload.PackageType == "msi" && (exitCode == 0 || exitCode == 3010 || exitCode == 1641) {
		// MSI 3010 / 1641 indicates successful installation with reboot needed
		isSuccess = true
	}

	if isSuccess {
		log.Info().Str("task_id", payload.TaskID).Int("exit_code", exitCode).Msg("package installed successfully")
		_ = ReportProgress(ctx, serverURL, deviceID, deviceSecret, ProgressReport{
			TaskID:    payload.TaskID,
			Status:    "success",
			ExitCode:  &exitCode,
			OutputLog: &output,
		})
		return nil
	}

	failMsg := ""
	if runErr != nil {
		failMsg = runErr.Error()
	} else {
		failMsg = fmt.Sprintf("installer exited with non-zero exit code: %d", exitCode)
	}
	log.Warn().Str("task_id", payload.TaskID).Int("exit_code", exitCode).Str("error", failMsg).Msg("installation failed")

	_ = ReportProgress(ctx, serverURL, deviceID, deviceSecret, ProgressReport{
		TaskID:       payload.TaskID,
		Status:       "failed",
		ExitCode:     &exitCode,
		OutputLog:    &output,
		ErrorMessage: &failMsg,
	})
	return errors.New(failMsg)
}

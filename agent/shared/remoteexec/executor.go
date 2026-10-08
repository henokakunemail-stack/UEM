package remoteexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/transport"
)

type ExecPayload struct {
	ExecutionID string `json:"execution_id"`
	Shell       string `json:"shell"`
	Command     string `json:"command"`
	TimeoutSec  int    `json:"timeout_sec"`
	// ReportURL is where the outcome goes. It cannot be derived from the
	// execution id: two different features dispatch the same "exec.run"
	// command, each with its own table and its own result endpoint. The task
	// scheduler sends this pointing at its own route, and remote-exec leaves
	// it empty and takes the default below.
	ReportURL string `json:"report_url,omitempty"`
}

type ExecReport struct {
	ExecutionID  string  `json:"execution_id"`
	Status       string  `json:"status"` // completed, failed, timeout, cancelled
	ExitCode     *int    `json:"exit_code,omitempty"`
	Output       *string `json:"output,omitempty"`
	ErrorMessage *string `json:"error_message,omitempty"`
}

type CommandRunner interface {
	RunCommand(ctx context.Context, shell, command string) (exitCode int, output string, err error)
}

// runningCommands tracks active command executions by execution_id
// so they can be cancelled via exec.cancel command.
var (
	runningCommandsMu sync.Mutex
	runningCommands   = make(map[string]context.CancelFunc)
)

// ToHTTPURL normalizes ws:// and wss:// to http:// and https://.
func ToHTTPURL(serverURL string) string {
	if strings.HasPrefix(serverURL, "wss://") {
		return "https://" + strings.TrimPrefix(serverURL, "wss://")
	}
	if strings.HasPrefix(serverURL, "ws://") {
		return "http://" + strings.TrimPrefix(serverURL, "ws://")
	}
	return strings.TrimRight(serverURL, "/")
}

// ReportResult posts the command execution outcome back to the server.
//
// reportPath is the route the caller asked for, or empty for the remote-exec
// default. See ExecPayload.ReportURL for why the destination cannot be guessed
// from the execution id alone.
func ReportResult(ctx context.Context, serverURL, deviceID, deviceSecret, reportPath string, rep ExecReport) error {
	apiBase := ToHTTPURL(serverURL)
	b, err := json.Marshal(rep)
	if err != nil {
		return err
	}

	if reportPath == "" {
		reportPath = "/api/agent/executions/" + rep.ExecutionID + "/result"
	}
	reqURL := apiBase + reportPath
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
		return fmt.Errorf("server rejected execution report: status %d", resp.StatusCode)
	}
	return nil
}

// ExecuteAndReport runs a remote command in the background and reports results.
func ExecuteAndReport(ctx context.Context, serverURL, deviceID, deviceSecret string, rawPayload json.RawMessage) error {
	var payload ExecPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}

	if payload.ExecutionID == "" || payload.Command == "" {
		return errors.New("missing execution_id or command")
	}

	timeout := 60 * time.Second
	if payload.TimeoutSec > 0 {
		timeout = time.Duration(payload.TimeoutSec) * time.Second
	}

	log.Info().
		Str("exec_id", payload.ExecutionID).
		Str("shell", payload.Shell).
		Str("command", payload.Command).
		Dur("timeout", timeout).
		Msg("starting remote execution")

	execCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Register this execution so it can be cancelled
	runningCommandsMu.Lock()
	runningCommands[payload.ExecutionID] = cancel
	runningCommandsMu.Unlock()

	defer func() {
		runningCommandsMu.Lock()
		delete(runningCommands, payload.ExecutionID)
		runningCommandsMu.Unlock()
	}()

	runner := newPlatformRunner()
	exitCode, output, err := runner.RunCommand(execCtx, payload.Shell, payload.Command)

	rep := ExecReport{
		ExecutionID: payload.ExecutionID,
		ExitCode:    &exitCode,
		Output:      &output,
	}

	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			rep.Status = "timeout"
			errMsg := fmt.Sprintf("execution timed out after %v", timeout)
			rep.ErrorMessage = &errMsg
		} else if errors.Is(execCtx.Err(), context.Canceled) {
			rep.Status = "cancelled"
			errMsg := "execution cancelled by operator"
			rep.ErrorMessage = &errMsg
		} else {
			rep.Status = "failed"
			errMsg := err.Error()
			rep.ErrorMessage = &errMsg
		}
	} else if exitCode == 0 {
		rep.Status = "completed"
	} else {
		rep.Status = "failed"
		errMsg := fmt.Sprintf("command exited with non-zero code %d", exitCode)
		rep.ErrorMessage = &errMsg
	}

	log.Info().
		Str("exec_id", payload.ExecutionID).
		Str("status", rep.Status).
		Int("exit_code", exitCode).
		Msg("remote execution finished, reporting result")

	reportCtx, reportCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer reportCancel()

	return ReportResult(reportCtx, serverURL, deviceID, deviceSecret, payload.ReportURL, rep)
}

// HandleCancel processes an exec.cancel command for a running execution.
func HandleCancel(executionID string) {
	runningCommandsMu.Lock()
	cancelFn, ok := runningCommands[executionID]
	runningCommandsMu.Unlock()

	if ok {
		log.Info().Str("exec_id", executionID).Msg("cancelling execution")
		cancelFn()
	} else {
		log.Info().Str("exec_id", executionID).Msg("cancel requested but execution not found or already finished")
	}
}

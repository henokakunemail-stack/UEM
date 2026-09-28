// Package maintenance is the agent-side executor for server-issued device
// maintenance. Every operation here runs privileged against the local OS, so
// the design rule is narrow and absolute: the task type is checked against an
// exact-match allowlist before anything is scheduled, every subprocess is
// spawned argv-only with no shell string ever built from a server payload, and
// deletion is file-scoped only — os.Remove on a regular file, or on a directory
// that is provably empty.
package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/transport"
)

const (
	TaskCleanupTemp    = "cleanup_temp"
	TaskDiskCheck      = "disk_check"
	TaskMemoryHygiene  = "memory_hygiene"
	TaskLogMaintenance = "log_maintenance"
	TaskServiceCleanup = "service_cleanup"
	TaskFullScan       = "full_scan"
)

// Agent-facing report statuses. These are the six TaskStatus* values the
// server's isTaskStatus admits; an agent posts only 'running' (non-terminal,
// any step) and 'completed'|'failed' (terminal, final step only). There is no
// aggregate 'partial' — job-level partial is derived server-side.
const (
	statusRunning   = "running"
	statusCompleted = "completed"
	statusFailed    = "failed"
)

// allowed is the dispatch allowlist. A server-supplied task_type that is not a
// key here never reaches a switch case, an argv, or a filesystem path. It is a
// map, not a slice, so the lookup is an exact key comparison: case, whitespace
// and separator variants are all misses.
var allowed = map[string]struct{}{
	TaskCleanupTemp:    {},
	TaskDiskCheck:      {},
	TaskMemoryHygiene:  {},
	TaskLogMaintenance: {},
	TaskServiceCleanup: {},
	TaskFullScan:       {},
}

// fullScanSteps is the step order for full_scan. The agent posts 'running'
// after each of the first three and a terminal status after the fourth, so the
// console shows the last step reached and the server's per-step accumulator
// sums all four deltas.
var fullScanSteps = []string{
	TaskCleanupTemp, TaskMemoryHygiene, TaskLogMaintenance, TaskDiskCheck,
}

// StepRequest is the WebSocket command payload for "maintenance.run".
type StepRequest struct {
	TaskID   string `json:"task_id"`
	TaskType string `json:"task_type"`
}

// StepReport mirrors server/modules/maintenance/model.go:149-158 field for
// field, including the omitempty tags. The agent does not import server code —
// Go excludes test-only imports from non-test builds, but a non-test import
// would drag the whole server module graph into the agent binary.
type StepReport struct {
	TaskID         string  `json:"task_id"`
	Step           string  `json:"step"`
	Status         string  `json:"status"`
	ExitCode       *int    `json:"exit_code,omitempty"`
	OutputLog      *string `json:"output_log,omitempty"`
	ErrorMessage   *string `json:"error_message,omitempty"`
	RebootRequired bool    `json:"reboot_required,omitempty"`
	BytesFreed     int64   `json:"bytes_freed,omitempty"`
}

type stepOutcome struct {
	output       string
	exitCode     int
	bytesFreed   int64
	rebootNeeded bool
}

// Engine runs maintenance tasks for one device. mu is a TryLock, not a Lock:
// two concurrent privileged sweeps on one box is a class of bug, so the second
// one is rejected outright rather than queued behind the first.
type Engine struct {
	serverBase   string
	deviceID     string
	deviceSecret string
	client       *http.Client
	mu           sync.Mutex
	// runStep is the one seam a test replaces, so Run's reporting sequence can
	// be exercised without deleting real temp files or touching real memory.
	// Production leaves it nil and Run uses runStepOS.
	runStep func(ctx context.Context, step string) (stepOutcome, error)
}

func NewEngine(serverBase, deviceID, deviceSecret string) *Engine {
	return &Engine{
		serverBase:   strings.TrimRight(serverBase, "/"),
		deviceID:     deviceID,
		deviceSecret: deviceSecret,
		client:       transport.NewHTTPClient(30 * time.Second),
	}
}

// Allowed reports whether task_type may run on this device. Exact map lookup —
// no trimming, no lowercasing.
func Allowed(taskType string) bool {
	_, ok := allowed[taskType]
	return ok
}

func Capabilities() []string { return []string{"maintenance.run"} }

// Run executes one maintenance task and reports every step over HTTP. The
// socket command_result reply is not the result channel: it is one-shot and
// keyed to the command id, and a four-step full_scan outlives it.
func (e *Engine) Run(ctx context.Context, raw json.RawMessage) error {
	var params StepRequest
	if err := json.Unmarshal(raw, &params); err != nil {
		return fmt.Errorf("decode maintenance request: %w", err)
	}
	if params.TaskID == "" {
		return errors.New("maintenance request is missing task_id")
	}
	// The authoritative allowlist check, before any privileged work is
	// scheduled. The dispatcher's gate in main.go is a courtesy; this is the
	// one that must not be bypassed.
	if !Allowed(params.TaskType) {
		return fmt.Errorf("unsupported maintenance task type %q", params.TaskType)
	}

	if !e.mu.TryLock() {
		return errors.New("another maintenance task is already running on this device")
	}
	defer e.mu.Unlock()

	steps := []string{params.TaskType}
	if params.TaskType == TaskFullScan {
		steps = fullScanSteps
	}

	exec := e.runStep
	if exec == nil {
		exec = runStepOS
	}

	for i, step := range steps {
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeout(step))
		out, err := exec(stepCtx, step)
		cancel()

		status := statusCompleted
		if err != nil {
			status = statusFailed
			out.output = strings.TrimSpace(out.output + "\n" + err.Error())
			if out.exitCode == 0 {
				out.exitCode = 1
			}
		}

		last := i == len(steps)-1
		// OutputLog and ErrorMessage are *string on both sides, so each needs a
		// distinct addressable variable. The server treats an absent log as an
		// empty one, so an empty transcript is still worth sending: the console
		// renders it verbatim and a silent row is indistinguishable from a
		// sweep that did nothing.
		outputLog := truncate(out.output, 8*1024)
		rep := StepReport{
			TaskID:         params.TaskID,
			Step:           step,
			Status:         status,
			ExitCode:       &out.exitCode,
			OutputLog:      &outputLog,
			RebootRequired: out.rebootNeeded,
			BytesFreed:     out.bytesFreed,
		}
		if err != nil {
			errMsg := truncate(err.Error(), 2*1024)
			rep.ErrorMessage = &errMsg
		}
		if !last {
			// Non-terminal for every step but the last. Posting a terminal
			// status here would close the task and the server would absorb
			// every remaining step.
			rep.Status = statusRunning
		}
		if err := e.report(ctx, rep); err != nil {
			log.Error().Err(err).
				Str("task_id", params.TaskID).Str("step", step).
				Msg("send maintenance step report")
		}
		// A failed step does not abort the sweep: the remaining steps still run
		// and report, which is what makes a partial sweep visible instead of
		// all-or-nothing.
	}
	return nil
}

func (e *Engine) report(ctx context.Context, rep StepReport) error {
	url := e.serverBase + "/api/agent/maintenance/tasks/" + rep.TaskID + "/result"
	body, err := json.Marshal(rep)
	if err != nil {
		return fmt.Errorf("marshal maintenance report: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create report request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The per-device secret, not a user JWT. These header names are the ones
	// devicemgmt.AuthenticateAgent reads.
	req.Header.Set("X-Device-Id", e.deviceID)
	req.Header.Set("X-Device-Secret", e.deviceSecret)

	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("send maintenance report: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("maintenance report rejected with status %d", resp.StatusCode)
	}
	return nil
}

// run is argv-only. It never accepts a shell string, and nothing from a server
// payload is ever concatenated into one: every call site passes a compile-time
// name and literal arguments.
func run(ctx context.Context, name string, args ...string) stepOutcome {
	if path, err := exec.LookPath(name); err == nil {
		name = path
	}
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	code := 0
	if err != nil {
		code = 1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	return stepOutcome{output: buf.String(), exitCode: code}
}

// stepTimeout is the per-step ceiling. disk_check is slow on a large or
// fragmented volume; memory_hygiene is a few seconds of work at most.
func stepTimeout(step string) time.Duration {
	switch step {
	case TaskDiskCheck:
		return 15 * time.Minute
	case TaskCleanupTemp, TaskLogMaintenance, TaskFullScan:
		return 10 * time.Minute
	case TaskMemoryHygiene:
		return 2 * time.Minute
	default:
		return time.Minute
	}
}

// dirSize sums the apparent size of regular files under root, skipping files
// newer than minAge. Symlinks are not followed. The first real error is
// returned so a failed walk is distinguishable from a clean one — a (0, nil)
// on an unreadable tree would report a green sweep that freed nothing because
// nothing was scanned.
func dirSize(root string, minAge time.Duration) (int64, error) {
	var total int64
	var firstErr error
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		// One Info() call feeds both the age test and the size. Calling it twice
		// means a failure on the age check falls through and counts the file it
		// should have skipped.
		info, e := d.Info()
		if e != nil {
			if firstErr == nil {
				firstErr = e
			}
			return nil
		}
		if minAge > 0 && time.Since(info.ModTime()) < minAge {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, firstErr
}

// removeOldFiles deletes regular files directly under root that match match and
// are at least minAge old. It never recurses: a directory under one of these
// roots is left alone unless it is itself empty (see removeEmptyDirs). Returns
// the number of files it deleted and the first error that was not a benign
// access-denied.
func removeOldFiles(root string, minAge time.Duration, match func(name string) bool) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	removed := 0
	var firstErr error
	for _, entry := range entries {
		name := entry.Name()
		if !match(name) || entry.IsDir() || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if minAge > 0 && time.Since(info.ModTime()) < minAge {
			continue
		}
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			// A file in use, or one the process is not permitted to delete, is
			// normal on a live machine. Record it and keep going: a sweep that
			// aborts on the first locked DLL is a sweep that never runs.
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// removeEmptyDirs is the only directory deletion in the package. os.Remove on a
// directory succeeds only when it is empty, so a non-empty directory is a
// no-op failure rather than a recursive delete. This is the deliberate
// alternative to os.RemoveAll everywhere else in the file.
func removeEmptyDirs(root string, minAge time.Duration) (int, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	removed := 0
	var firstErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if minAge > 0 && time.Since(info.ModTime()) < minAge {
			continue
		}
		if err := os.Remove(dir); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// freedBytes is max(0, before-after). Measuring the size twice around the
// actual deletion is deliberate: measuring and deleting in the same walk is
// faster, but a partial delete would then report zero for work that did happen.
func freedBytes(before, after int64) int64 {
	if after >= before {
		return 0
	}
	return before - after
}

// truncate bounds an agent-supplied string before it goes over the wire. It
// byte-slices, so a multi-byte rune can be cut; output logs are terminal text
// rendered in a <pre>, so a trailing replacement byte is cosmetic.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// appendLog concatenates a subprocess transcript onto an accumulated step
// output without dropping what is already there.
func appendLog(dst string, out stepOutcome) string {
	if out.output == "" {
		return dst
	}
	if dst == "" {
		return out.output
	}
	return dst + "\n" + out.output
}

// assertDistinctMirrors fails the build if a copy-paste typo makes two mirrors
// equal. Referenced from maintenance_test.go so golangci-lint's `unused` check
// does not flag it.
func assertDistinctMirrors() {
	var s string
	switch s {
	case TaskCleanupTemp:
	case TaskDiskCheck:
	case TaskMemoryHygiene:
	case TaskLogMaintenance:
	case TaskServiceCleanup:
	case TaskFullScan:
	}
}

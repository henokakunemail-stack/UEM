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
	TaskCleanupTemp     = "cleanup_temp"
	TaskDiskCheck       = "disk_check"
	TaskMemoryHygiene   = "memory_hygiene"
	TaskLogMaintenance  = "log_maintenance"
	TaskServiceCleanup  = "service_cleanup"
	TaskFlushDNS        = "flush_dns"
	TaskSecurityAudit   = "security_audit"
	TaskSystemIntegrity = "system_integrity"
	TaskFullScan        = "full_scan"
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
	TaskCleanupTemp:     {},
	TaskDiskCheck:       {},
	TaskMemoryHygiene:   {},
	TaskLogMaintenance:  {},
	TaskServiceCleanup:  {},
	TaskFlushDNS:        {},
	TaskSecurityAudit:   {},
	TaskSystemIntegrity: {},
	TaskFullScan:        {},
}

// fullScanSteps is the step order for full_scan. The agent posts 'running'
// after each of the first three and a terminal status after the fourth, so the
// console shows the last step reached and the server's per-step accumulator
// sums all four deltas.
var fullScanSteps = []string{
	TaskCleanupTemp, TaskMemoryHygiene, TaskLogMaintenance, TaskDiskCheck,
}

// stepsFor mirrors the server's StepsForTaskType. Every task type is its own
// single step except full_scan, which expands into the four steps above. Run
// walks this list; reportBusy reads its first element to name the step it
// reports a rejection against.
func stepsFor(taskType string) []string {
	if taskType == TaskFullScan {
		return fullScanSteps
	}
	return []string{taskType}
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
	TaskID   string `json:"task_id"`
	Step     string `json:"step"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code,omitempty"`
	// Heartbeat is the liveness flag the server's orphan sweep depends on. See
	// server/modules/maintenance/model.go:185 — the same comment lives on both
	// sides, and TestStepReportFieldTagsMatchServer keeps them from drifting.
	Heartbeat      bool    `json:"heartbeat,omitempty"`
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
	// timedOut records that at least one command in this step was stopped by
	// its own ceiling rather than exiting on its own. It is what separates "the
	// tool refused" from "we gave up waiting", which the exit code alone cannot:
	// both arrive as a non-zero code.
	timedOut bool
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
//
// id is the envelope id. The server puts task_id in the payload, but a command
// whose payload omits it still names the task it belongs to, and losing that
// leaves the server row dispatched forever — so the fallback is resolved here,
// once, rather than in each command handler.
func (e *Engine) Run(ctx context.Context, raw json.RawMessage, id string) error {
	var params StepRequest
	if err := json.Unmarshal(raw, &params); err != nil {
		return fmt.Errorf("decode maintenance request: %w", err)
	}
	if params.TaskID == "" {
		params.TaskID = id
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
		// The server already marked this task 'dispatched' and is waiting for
		// a report. A local log line gives that row no path to a terminal
		// state, so the rejection is posted the same way a step is — as a
		// failed report for the step this task would have run.
		e.reportBusy(ctx, params.TaskID, params.TaskType)
		return errors.New("another maintenance task is already running on this device")
	}
	defer e.mu.Unlock()

	steps := stepsFor(params.TaskType)

	exec := e.runStep
	if exec == nil {
		exec = runStepOS
	}

	// anyFailed carries a failure from an earlier step to the final report.
	// Only the last step may post a terminal status: the server closes the
	// task on the first terminal one and absorbs every later report
	// (repository.go:567), and a 'failed' posted mid-run would lose the bytes
	// of the steps that follow. So the verdict has to be assembled here and
	// attached to the one report the server does accept — otherwise a full_scan
	// whose first step refused every root still ends 'completed', because the
	// last step alone decided the outcome.
	var anyFailed bool
	var firstExit int
	var firstStep string

	for i, step := range steps {
		out, err := e.runWithCeiling(ctx, params.TaskID, step, exec, stepTimeout(step))

		status := statusCompleted
		switch {
		case err != nil:
			status = statusFailed
			out.output = strings.TrimSpace(out.output + "\n" + err.Error())
			if out.exitCode == 0 {
				out.exitCode = 1
			}
		case out.exitCode != 0:
			// A step that returns (stepOutcome, nil) with a non-zero exit
			// code is telling us it did not finish, and it is telling us so
			// in the only channel available: the code. Two shapes need this.
			// disk_check runs chkdsk and defrag, which print "Access is
			// denied ... elevated mode" rather than raising, so the Go error
			// path never fires. cleanup_temp marks a refused root itself, and
			// used to do it with exitCode 0, which reported a completed sweep
			// that had cleaned nothing.
			//
			// memory_hygiene and log_maintenance deliberately record a refused
			// subprocess in the transcript and stay green: they still did the
			// correct thing on a machine they could not fully act on, and
			// reddening every endpoint for that teaches operators to ignore
			// the alert. Those steps do not set a non-zero code on themselves.
			status = statusFailed
			err = fmt.Errorf("step did not complete (exit %d): %s",
				out.exitCode, failureCause(step, out))
			// Also appended to the transcript, below the cause and behind
			// truncateLog's tail: an operator reading only the log still sees it.
			out.output = strings.TrimSpace(out.output + "\n" + err.Error())
		}
		if status == statusFailed {
			anyFailed = true
			// The first non-zero code wins: it names the step that broke the
			// run, which is the one an operator needs to look at.
			if firstExit == 0 {
				firstExit = out.exitCode
				firstStep = step
			}
		}

		last := i == len(steps)-1
		// OutputLog and ErrorMessage are *string on both sides, so each needs a
		// distinct addressable variable. The server treats an absent log as an
		// empty one, so an empty transcript is still worth sending: the console
		// renders it verbatim and a silent row is indistinguishable from a
		// sweep that did nothing.
		outputLog := truncateLog(out.output, 8*1024)
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
		} else if anyFailed && status != statusFailed {
			// The run as a whole failed even though this step did not. The
			// status and the exit code have to agree, or the console shows a
			// red pill next to exit 0 and reads as a broken row.
			rep.Status = statusFailed
			rep.ExitCode = &firstExit
			msg := fmt.Sprintf("%s failed earlier in this task; see output log", firstStep)
			rep.ErrorMessage = &msg
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

// heartbeatInterval is how often a long step says it is still alive. It has to
// be well under the server's maintenance AbandonGrace (20 minutes) — that grace
// is measured from the last write to the task row, and the only thing that
// writes it during a step is a step report. A 10-minute tick leaves room for
// two missed reports plus a slow round trip before the sweep is entitled to
// conclude the agent is gone.
const heartbeatInterval = 10 * time.Minute

// heartbeatEvery is the interval the engine actually uses. It is a variable and
// not the constant directly so a test can drive the loop in milliseconds; the
// production path never assigns it.
var heartbeatEvery = heartbeatInterval

// runWithCeiling runs one step under its ceiling while posting a 'running'
// report every heartbeatInterval until it returns.
//
// This exists because the two numbers that were supposed to keep each other in
// check did not. A step may now run for up to 30 minutes, and the sweep reaps a
// task that has not been written to for 20 — so a slow-but-healthy disk check
// would be killed by the server roughly ten minutes in, and the console would
// show the sweep as abandoned while the agent was still chkdsking through it.
// The heartbeat is what makes the ceiling safe to raise: the sweep's evidence
// that an agent is still working is the agent saying so, and before this a step
// was silent for its entire duration.
//
// The heartbeat sets a dedicated flag rather than posting a bare 'running' for
// the step in flight, and that is not a cosmetic choice. RecordStep's ordinary
// path guards its byte counter with `bytes_freed = bytes_freed + CASE WHEN
// step <> ? THEN ? ELSE 0 END`, which reads the step column to ask "has this
// step already been counted?" — so a plain 'running' for the in-flight step
// would advance the step column, and the step's own terminal report would then
// match it and read as a replay. Its bytes would be dropped, silently, on a
// sweep that would then report having freed less than it did. A heartbeat takes
// an early return in RecordStep that moves updated_at and nothing else.
func (e *Engine) runWithCeiling(
	ctx context.Context,
	taskID, step string,
	exec func(context.Context, string) (stepOutcome, error),
	limit time.Duration,
) (stepOutcome, error) {
	stepCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	type result struct {
		out stepOutcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := exec(stepCtx, step)
		done <- result{out, err}
	}()

	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case r := <-done:
			return r.out, r.err
		case <-ticker.C:
			// Nothing has changed about the step; this is a liveness signal
			// only. The log is left empty and the server advances no column but
			// updated_at, so the step the console is showing, the bytes already
			// counted, and the transcript on screen all survive it.
			if err := e.report(ctx, StepReport{
				TaskID:    taskID,
				Step:      step,
				Status:    statusRunning,
				Heartbeat: true,
			}); err != nil {
				// Logged, not fatal. The step is still running and still has a
				// terminal report to post; failing here would kill a healthy
				// scan because a heartbeat happened to be dropped.
				log.Error().Err(err).
					Str("task_id", taskID).Str("step", step).
					Msg("send maintenance step heartbeat")
			}
		case <-stepCtx.Done():
			// The ceiling arrived. Wait for the step to unwind so its partial
			// output and exit code are still reported — a goroutine that has not
			// returned yet is one that has not noticed the cancel yet, and both
			// sides of the race produce the same answer once it does.
			//
			// ponytail: this can block past the ceiling, because a step is not
			// required to poll its context (the dirSize walk does not). That is
			// the same hang the pre-heartbeat code had, since it called exec
			// synchronously. Every subprocess path does honour it; the walk is
			// the outlier and gets ctx-aware when it is worth the plumbing.
			r := <-done
			return r.out, r.err
		}
	}
}

// failureCause names why a step did not finish, in the one sentence an operator
// reads before deciding what to do about it.
//
// It used to say the same thing for every non-zero exit: "install the agent as
// a Windows Service so it runs elevated". That was wrong often enough to be
// worse than silence. On a properly elevated agent a chkdsk that outran its
// budget was reported as a missing service, and the operator's next move — go
// install the service — could not have changed anything, because the service was
// already there and already LocalSystem. A reason that is confidently false
// costs more than no reason, so every cause here is one this process actually
// observed rather than one it guessed at.
func failureCause(step string, out stepOutcome) string {
	// The one cause that is genuinely this process's own doing, and the only
	// one it can prove: a command was still running when its ceiling arrived.
	if out.timedOut {
		return fmt.Sprintf("%s did not finish inside its time limit and was stopped; "+
			"re-run it, or check the transcript for which command stalled", step)
	}
	// Windows prints its own refusal text, and it is specific enough to quote.
	// chkdsk and defrag both say "running in elevated mode" when they mean
	// elevation; matching on it costs nothing and rules the cause in or out
	// instead of asserting it.
	if strings.Contains(out.output, "elevated mode") ||
		strings.Contains(out.output, "Access is denied") {
		return "the operating system refused the request for want of privilege; " +
			"install the agent as a Windows Service so it runs elevated"
	}
	// cleanup_temp writes its own summary line naming the refused roots, and it
	// is the accurate version of the above — repeating a generic privilege line
	// next to it would contradict a line that already has the specifics.
	if strings.Contains(out.output, "cleanup roots were refused") {
		return "one or more cleanup roots were refused; see the transcript for which"
	}
	return fmt.Sprintf("%s reported exit %d; the transcript above has its output",
		step, out.exitCode)
}

// reportBusy closes a task the engine refused to start. A rejection reason the
// server never sees is the same defect as a step that is never reported: the
// job keeps counting a dispatched task as in flight forever. The report is
// deliberately a failure rather than a skip — 'skipped' is server-set for an
// offline target, and RecordStep rejects an agent that claims it.
func (e *Engine) reportBusy(ctx context.Context, taskID, taskType string) {
	code := 1
	reason := "another maintenance task is already running on this device"
	logText := "not started: " + reason
	// The reported step has to be one the task could have produced. For every
	// single-step task that is the task type itself, but full_scan is a
	// composite: the server resolves a step against the task's step sequence,
	// where "full_scan" is absent, and rejects the report as a step that does
	// not belong. That rejection is what strands the row: the agent logged
	// nothing, the server stored nothing, and the job counted a dispatched task
	// for ever. Reporting the first step of the sequence is what the server
	// accepts, and it is the step the task would have started with anyway.
	step := taskType
	if steps := stepsFor(taskType); len(steps) > 0 {
		step = steps[0]
	}
	if err := e.report(ctx, StepReport{
		TaskID:       taskID,
		Step:         step,
		Status:       statusFailed,
		ExitCode:     &code,
		OutputLog:    &logText,
		ErrorMessage: &reason,
	}); err != nil {
		log.Error().Err(err).Str("task_id", taskID).
			Msg("report rejected maintenance task")
	}
}

// errPermanent marks a report the server refused on purpose. A 4xx means the
// same payload will be refused again, and a marshal or request-build failure
// will never succeed on a retry — repeating those only burns the window the
// step still has left.
type errPermanent struct{ error }

// reportAttempts and reportBackoff bound the retry of a TRANSIENT failure. One
// dropped POST used to lose a step permanently: the row stayed 'dispatched',
// the job stayed 'running', and the console polled both forever. Three tries
// over ~700ms rides out a server restart or a single 502 without ever holding
// the sweep for a second longer than that.
const (
	reportAttempts = 3
)

func reportBackoff(attempt int) time.Duration {
	return 100 * time.Millisecond << (attempt - 1)
}

// report posts one step report, retrying only a transient failure.
func (e *Engine) report(ctx context.Context, rep StepReport) error {
	var last error
	for attempt := 1; attempt <= reportAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(reportBackoff(attempt - 1)):
			}
		}
		last = e.reportOnce(ctx, rep)
		if last == nil {
			return nil
		}
		var permanent *errPermanent
		if errors.As(last, &permanent) {
			return last
		}
	}
	return last
}

func (e *Engine) reportOnce(ctx context.Context, rep StepReport) error {
	url := e.serverBase + "/api/agent/maintenance/tasks/" + rep.TaskID + "/result"
	body, err := json.Marshal(rep)
	if err != nil {
		return &errPermanent{fmt.Errorf("marshal maintenance report: %w", err)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &errPermanent{fmt.Errorf("create report request: %w", err)}
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
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return &errPermanent{fmt.Errorf("maintenance report rejected with status %d", resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("maintenance report rejected with status %d", resp.StatusCode)
	}
	return nil
}

// commandTimeout is the per-command ceiling inside one step. The step timeout
// is the budget for the whole step, which several commands share, so a single
// one of them would otherwise be able to consume all of it and leave the
// console showing no progress at all.
//
// It is a default, not a verdict. The commands that legitimately run for a
// quarter of an hour are long ones — chkdsk /scan and Dism /StartComponentCleanup
// both scale with the size of the machine — and a flat ceiling shorter than
// their real runtime does not fail them, it kills them and reports the failure
// as something it is not. runLong, below, is the opt-in for those.
//
// Clear-RecycleBin is the opposite case and is why the default is not generous:
// it sits on a full recycle bin long enough to matter, which was measured at
// over 20 seconds here and grows with the bin. A bin that will not empty is a
// lesser outcome than a sweep that never finishes.
const commandTimeout = 90 * time.Second

// commandCeiling is the flat ceiling run actually enforces. It is a variable
// and not commandTimeout directly for the same reason heartbeatEvery is not
// heartbeatInterval: the property that a long command is not killed by the flat
// ceiling can only be tested in milliseconds, since asserting it at production
// scale would mean waiting 90 seconds to watch a command survive. The
// production path never assigns it.
var commandCeiling = commandTimeout

// run is argv-only. It never accepts a shell string, and nothing from a server
// payload is ever concatenated into one: every call site passes a compile-time
// name and literal arguments.
//
// The ceiling here is the flat commandTimeout. It is a ceiling and not a
// budget: the caller's context still bounds it, so a command can never outlive
// its step.
func run(ctx context.Context, name string, args ...string) stepOutcome {
	return runFor(ctx, commandCeiling, name, args...)
}

// runLong is run for a command that is allowed to take the whole step. It is
// named at the call site rather than derived from a table so that reading
// windowsDiskCheck tells you which of its commands are unbounded without a
// lookup, and so a new long command cannot be added by accident.
func runLong(ctx context.Context, name string, args ...string) stepOutcome {
	return runFor(ctx, 0, name, args...)
}

// runFor is the shared body. A zero ceiling means "whatever the caller's
// context allows", which is how a long command gets the whole step while still
// being killed by it.
func runFor(ctx context.Context, limit time.Duration, name string, args ...string) stepOutcome {
	if path, err := exec.LookPath(name); err == nil {
		name = path
	}
	runCtx := ctx
	if limit > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	var buf bytes.Buffer
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := stepOutcome{}
	if err != nil {
		out.exitCode = 1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			out.exitCode = ee.ExitCode()
		}
		// Exceeded the budget rather than exiting: say so in the transcript,
		// because "exit 1" and "gave up waiting" are different things for
		// whoever reads the maintenance log, and only one of them is fixed by
		// running the agent elevated.
		if runCtx.Err() != nil {
			out.timedOut = true
			if limit > 0 {
				buf.WriteString("\ncommand exceeded the " +
					limit.String() + " limit and was stopped")
			} else {
				buf.WriteString("\ncommand exceeded its step's time limit and was stopped")
			}
		}
	}
	out.output = buf.String()
	return out
}

// stepTimeout is the per-step ceiling. disk_check is slow on a large or
// fragmented volume; memory_hygiene is a few seconds of work at most.
//
// ponytail: these ceilings are a backstop, not the mechanism. Every step heart
// beats while it works, so the sweep no longer reaps a long scan. Raise a
// ceiling only when a command on the slowest supported machine genuinely
// exceeds it — and when you do, check it against maintenance.AbandonGrace on
// the server, which is the only other number this interacts with.
func stepTimeout(step string) time.Duration {
	switch step {
	case TaskDiskCheck:
		return 30 * time.Minute
	case TaskCleanupTemp, TaskLogMaintenance, TaskFullScan:
		return 20 * time.Minute
	case TaskSystemIntegrity:
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
// rendered in a <pre>, so a replacement byte is cosmetic.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// truncateLog bounds an output log, keeping its head AND its tail.
//
// The head is what says what was being attempted: chkdsk's first lines name the
// volume, the file system, and the flags. The tail is what says how it ended,
// and it is where a command killed by its ceiling announces itself. A
// head-only cut loses the second of those — which is how a transcript came to
// end mid-progress-bar with nothing saying why it stopped. Worse, the tail is
// also where the reason this step failed was appended, so a head-only cut
// discarded the explanation along with it.
//
// A plain byte-slice cannot give both without deciding the seam by hand, so the
// two halves are joined with an explicit marker; the marker is part of the
// budget rather than added on top of it, so the result is exactly max bytes.
//
// ponytail: 1/3 head, 2/3 tail, split by a constant. A byte-accurate seam
// would survive a UTF-8 boundary better, but both cuts can already land inside
// a rune and the text is terminal output in a <pre>. Revisit only if a
// transcript ever has to be machine-parsed rather than read.
func truncateLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const marker = "\n... [transcript truncated] ...\n"
	// Not max/2: the tail is the half that carries the verdict, so it gets the
	// larger share, but only once the head has proven long enough to be worth
	// most of the budget.
	if len(marker) >= max {
		return truncate(s, max)
	}
	keep := max - len(marker)
	head, tail := keep/3, keep-keep/3
	return s[:head] + marker + s[len(s)-tail:]
}

// appendLog folds a subprocess outcome into an accumulating step outcome:
// the transcript is appended to the text and a non-zero subprocess exit code
// is carried into the step's own exit code.
//
// Folding the code here rather than at each call site is deliberate. Every
// caller used to keep only the text: `chkdsk /scan` on a non-elevated agent
// exits non-zero and prints "Access Denied ... You have to invoke this utility
// running in elevated mode", and the step still reported exit 0. A disk check
// that scanned nothing then looked identical to one that found a clean volume,
// which is the exact failure an operator cannot afford to be unable to see.
//
// The code only ever moves away from 0, so a later successful subprocess cannot
// paper over an earlier failure, and the first non-zero code is the one kept.
// timedOut latches the same way: once a command has been stopped, no later
// success in the same step makes the stop untrue.
func appendLog(acc stepOutcome, out stepOutcome) stepOutcome {
	if out.output != "" {
		if acc.output == "" {
			acc.output = out.output
		} else {
			acc.output += "\n" + out.output
		}
	}
	if out.exitCode != 0 && acc.exitCode == 0 {
		acc.exitCode = out.exitCode
	}
	if out.bytesFreed != 0 {
		acc.bytesFreed += out.bytesFreed
	}
	if out.rebootNeeded {
		acc.rebootNeeded = true
	}
	if out.timedOut {
		acc.timedOut = true
	}
	return acc
}

// appendSecondaryLog folds an auxiliary or optional tool outcome into the
// accumulating step outcome. Unlike appendLog, a non-zero exit code or timeout
// in a secondary tool logs a warning note to the transcript without failing
// the overall step or setting acc.exitCode/acc.timedOut.
func appendSecondaryLog(acc stepOutcome, out stepOutcome, name string) stepOutcome {
	if out.output != "" {
		if acc.output == "" {
			acc.output = out.output
		} else {
			acc.output += "\n" + out.output
		}
	}
	if out.bytesFreed != 0 {
		acc.bytesFreed += out.bytesFreed
	}
	if out.rebootNeeded {
		acc.rebootNeeded = true
	}
	if out.exitCode != 0 || out.timedOut {
		var warn string
		if out.timedOut {
			warn = fmt.Sprintf("warning: %s timed out and was stopped; primary cleanup succeeded", name)
		} else {
			warn = fmt.Sprintf("warning: %s failed (exit %d); primary cleanup succeeded", name, out.exitCode)
		}
		if acc.output == "" {
			acc.output = warn
		} else {
			acc.output += "\n" + warn
		}
	}
	return acc
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
	case TaskFlushDNS:
	case TaskSecurityAudit:
	case TaskSystemIntegrity:
	case TaskFullScan:
	}
}

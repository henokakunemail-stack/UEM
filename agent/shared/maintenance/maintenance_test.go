package maintenance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	srv "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/maintenance"
)

// TestMirrorsMatchServer keeps the agent's task-type constants identical to the
// server's. The agent deliberately does not import the server package in
// non-test code — the import below is test-only, so Go excludes it from the
// agent binary — but nothing else would catch a rename on one side.
func TestMirrorsMatchServer(t *testing.T) {
	pairs := [][2]string{
		{TaskCleanupTemp, srv.TaskCleanupTemp},
		{TaskDiskCheck, srv.TaskDiskCheck},
		{TaskMemoryHygiene, srv.TaskMemoryHygiene},
		{TaskLogMaintenance, srv.TaskLogMaintenance},
		{TaskServiceCleanup, srv.TaskServiceCleanup},
		{TaskFlushDNS, srv.TaskFlushDNS},
		{TaskSecurityAudit, srv.TaskSecurityAudit},
		{TaskSystemIntegrity, srv.TaskSystemIntegrity},
		{TaskFullScan, srv.TaskFullScan},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("agent mirror %q drifted from server %q", p[0], p[1])
		}
	}
}

// TestFullScanOrderMatchesServer guards the step SEQUENCE, not just the names.
// The server rejects a report whose step is not the successor of the one it has
// recorded, so an agent that ran the steps in a different order would have its
// later reports rejected as replays and its bytes_freed silently dropped.
func TestFullScanOrderMatchesServer(t *testing.T) {
	if len(fullScanSteps) != len(srv.FullScanStepOrder) {
		t.Fatalf("agent runs %d full_scan steps, server expects %d",
			len(fullScanSteps), len(srv.FullScanStepOrder))
	}
	for i := range fullScanSteps {
		if fullScanSteps[i] != srv.FullScanStepOrder[i] {
			t.Errorf("full_scan step %d: agent runs %q, server expects %q",
				i, fullScanSteps[i], srv.FullScanStepOrder[i])
		}
	}
}

// TestStepReportFieldTagsMatchServer is the same idea for the wire format: the
// JSON tags are what the server's StepReport decoder binds to, so a rename
// there is silent at compile time and fatal at runtime.
func TestStepReportFieldTagsMatchServer(t *testing.T) {
	agentShape := jsonTags(reflect.TypeOf(StepReport{}))
	serverShape := jsonTags(reflect.TypeOf(srv.StepReport{}))
	for name, want := range serverShape {
		got, ok := agentShape[name]
		if !ok {
			t.Errorf("agent StepReport is missing field %q", name)
			continue
		}
		if got != want {
			t.Errorf("agent StepReport.%s json tag = %q, server = %q", name, got, want)
		}
	}
	for name := range agentShape {
		if _, ok := serverShape[name]; !ok {
			t.Errorf("agent StepReport has field %q that the server does not", name)
		}
	}
}

// TestAllowedRejectsUnknown is the security-critical case. The allowlist is a
// map, so every one of these is a key miss rather than a prefix, a trim, or a
// fallback: an exact key match is the only thing that can schedule a
// privileged step.
func TestAllowedRejectsUnknown(t *testing.T) {
	for _, bad := range []string{
		"",
		" ",
		"FULL_SCAN",
		"Full_Scan",
		"full_scan ",
		" full_scan",
		"full_scan\n",
		"cleanup_temp; whoami",
		"cleanup_temp && calc",
		"../etc",
		"/etc/passwd",
		"disk_chec",
		"disk_checkk",
		"service_cleanup\x00",
	} {
		if Allowed(bad) {
			t.Errorf("allowlist accepted %q", bad)
		}
	}
	for _, good := range []string{
		TaskCleanupTemp, TaskDiskCheck, TaskMemoryHygiene,
		TaskLogMaintenance, TaskServiceCleanup, TaskFlushDNS,
		TaskSecurityAudit, TaskSystemIntegrity, TaskFullScan,
	} {
		if !Allowed(good) {
			t.Errorf("allowlist rejected valid task type %q", good)
		}
	}
	if len(allowed) != 9 {
		t.Errorf("allowlist size = %d, want 9", len(allowed))
	}
	assertDistinctMirrors() // keeps golangci-lint's `unused` check green
}

// TestRunRejectsBeforeAnyWork is the ordering guarantee: a missing or unknown
// task_id/type must return an error and must not have taken the engine lock.
func TestRunRejectsBeforeAnyWork(t *testing.T) {
	e := NewEngine("http://127.0.0.1:1/", "dev", "secret")

	for _, raw := range []string{
		`{"task_id":"t1","task_type":"rm -rf /"}`,
		`{"task_id":"t1","task_type":"cleanup_temp; whoami"}`,
		`{"task_id":"t1","task_type":"FULL_SCAN"}`,
		`{"task_id":"","task_type":"full_scan"}`,
		`not json`,
	} {
		// An empty envelope id: the fallback that fills a missing task_id from
		// the command id is covered on its own below, and reusing it here
		// would turn this case into a valid request.
		if err := e.Run(t.Context(), json.RawMessage(raw), ""); err == nil {
			t.Errorf("Run(%s) accepted a request it must reject", raw)
		}
	}
	// The lock must still be free: a rejected request never took it.
	if !e.mu.TryLock() {
		t.Error("rejected request left the engine lock held")
	} else {
		e.mu.Unlock()
	}
}

// TestRunAllowsOnlyOneTaskAtATime proves the TryLock. Holding the lock manually
// is the cheapest way to simulate a sweep already in flight.
func TestRunAllowsOnlyOneTaskAtATime(t *testing.T) {
	e := NewEngine("http://127.0.0.1:1/", "dev", "secret")
	e.mu.Lock()
	defer e.mu.Unlock()

	// task_type is valid here, so this reaches the TryLock and must fail there
	// rather than starting a second privileged sweep.
	if err := e.Run(t.Context(), json.RawMessage(`{"task_id":"t1","task_type":"disk_check"}`), "t1"); err == nil {
		t.Fatal("second concurrent maintenance task was not rejected")
	}
}

// TestABusyEngineReportsTheRejection is the reason Run posts its own failure.
// Without it the server row stays 'dispatched' for ever: the task is counted
// in flight, the job never closes, and the console shows a running sweep on a
// device that is waiting for nothing.
func TestABusyEngineReportsTheRejection(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.Run(t.Context(),
		json.RawMessage(`{"task_id":"t1","task_type":"disk_check"}`), "t1"); err == nil {
		t.Fatal("second concurrent maintenance task was not rejected")
	}

	if len(cs.reports) != 1 {
		t.Fatalf("posted %d reports on rejection, want 1", len(cs.reports))
	}
	rep := cs.reports[0]
	if rep.TaskID != "t1" {
		t.Errorf("rejection report task_id = %q, want t1", rep.TaskID)
	}
	if rep.Status != srv.TaskStatusFailed {
		t.Errorf("rejection report status = %q, want %q", rep.Status, srv.TaskStatusFailed)
	}
	// The step must belong to the task type or the server rejects the report
	// as a step it could never have produced, and the row strands again.
	if rep.Step != TaskDiskCheck {
		t.Errorf("rejection report step = %q, want %q", rep.Step, TaskDiskCheck)
	}
}

// TestABusyFullScanReportsItsFirstStep is the case the single-step busy test
// above cannot catch. disk_check is its own step, so reporting the task type
// happens to name a step the server accepts. full_scan is composite: the
// server resolves a reported step against the task's step sequence, in which
// "full_scan" does not appear, and RecordStep rejects it with "step does not
// belong to task type". The agent's report then never lands, the row stays
// dispatched, and the job counts a sweep as in flight for ever. Verified live:
// a full_scan dispatched onto an already-busy agent sat at 'dispatched' for
// 12 minutes with no transcript and no failure.
func TestABusyFullScanReportsItsFirstStep(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.Run(t.Context(),
		json.RawMessage(`{"task_id":"t2","task_type":"full_scan"}`), "t2"); err == nil {
		t.Fatal("second concurrent maintenance task was not rejected")
	}
	if len(cs.reports) != 1 {
		t.Fatalf("posted %d reports on rejection, want 1", len(cs.reports))
	}
	rep := cs.reports[0]
	if rep.Status != srv.TaskStatusFailed {
		t.Errorf("rejection report status = %q, want %q", rep.Status, srv.TaskStatusFailed)
	}
	if rep.Step != fullScanSteps[0] {
		t.Errorf("rejection report step = %q, want %q: the server resolves a "+
			"step against the task's own sequence and %q is not in it, so the "+
			"report is rejected and the row strands",
			rep.Step, fullScanSteps[0], TaskFullScan)
	}
	// The same check the server makes, run locally so the failure names the
	// rule rather than a 400 from the live endpoint.
	if got := srv.StepIndex(TaskFullScan, rep.Step); got < 0 {
		t.Errorf("server StepIndex(%q, %q) = %d, want >= 0", TaskFullScan, rep.Step, got)
	}
}

func TestFreedBytes(t *testing.T) {
	for _, c := range []struct{ before, after, want int64 }{
		{100, 40, 60},
		{0, 0, 0},
		{40, 100, 0}, // something else grew; report nothing rather than a negative
		{50, 50, 0},
	} {
		if got := freedBytes(c.before, c.after); got != c.want {
			t.Errorf("freedBytes(%d, %d) = %d, want %d", c.before, c.after, got, c.want)
		}
	}
}

// TestRemoveOldFilesIsFileScoped pins the deletion contract: only regular files
// matching the predicate and older than minAge go. A directory is never touched
// by this path, however empty it looks.
func TestRemoveOldFilesIsFileScoped(t *testing.T) {
	dir := t.TempDir()
	old := writeFile(t, dir, "old.tmp", "aaaaaaaa")
	recent := writeFile(t, dir, "new.tmp", "bbbb")
	skip := writeFile(t, dir, "old.keep", "cccc")
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	past := time.Now().Add(-48 * time.Hour)
	setModTime(t, old, past)
	setModTime(t, skip, past)
	// subdir is left recent on purpose: even an empty one is out of scope here.

	removed, err := removeOldFiles(dir, 24*time.Hour, func(name string) bool {
		return filepath.Ext(name) == ".tmp"
	})
	if err != nil {
		t.Fatalf("removeOldFiles: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old matching file survived")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("recent file was deleted despite minAge")
	}
	if _, err := os.Stat(skip); err != nil {
		t.Error("non-matching file was deleted despite the predicate")
	}
	if _, err := os.Stat(subdir); err != nil {
		t.Error("directory was touched by the file-scoped delete pass")
	}
}

// TestRemoveEmptyDirsNeverRecurses is the RemoveAll substitute. A directory
// holding one file must be left completely alone — os.Remove succeeding only on
// an empty directory is the whole safety property.
func TestRemoveEmptyDirsNeverRecurses(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	occupied := filepath.Join(dir, "occupied")
	for _, d := range []string{empty, occupied} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	keep := writeFile(t, occupied, "payload", "x")
	past := time.Now().Add(-90 * 24 * time.Hour)
	setModTime(t, empty, past)
	setModTime(t, occupied, past)

	removed, _ := removeEmptyDirs(dir, 30*24*time.Hour)
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (only the provably empty dir)", removed)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Error("empty dir survived")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("removeEmptyDirs recursed into a non-empty directory")
	}
}

func TestDirSizeSkipsNewFiles(t *testing.T) {
	dir := t.TempDir()
	old := writeFile(t, dir, "old", "1234567890")
	_ = writeFile(t, dir, "new", "x")
	setModTime(t, old, time.Now().Add(-48*time.Hour))

	total, err := dirSize(dir, 24*time.Hour)
	if err != nil {
		t.Fatalf("dirSize: %v", err)
	}
	if total != 10 {
		t.Errorf("dirSize = %d, want 10 (the recent file and its bytes excluded)", total)
	}
}

func TestDirSizeErrorsOnMissingRoot(t *testing.T) {
	// (0, nil) on a missing root would report a clean sweep that scanned
	// nothing. The first error is what distinguishes the two.
	if _, err := dirSize(filepath.Join(t.TempDir(), "nope"), 0); err == nil {
		t.Error("dirSize on a missing root returned no error")
	}
}

func TestStepTimeoutOrdering(t *testing.T) {
	// disk_check is the slow one, memory_hygiene is seconds. A swap here would
	// mean a long scan gets killed by the memory-hygiene ceiling.
	if stepTimeout(TaskDiskCheck) <= stepTimeout(TaskMemoryHygiene) {
		t.Error("disk_check timeout is not greater than memory_hygiene")
	}
	for _, step := range []string{
		TaskCleanupTemp, TaskDiskCheck, TaskMemoryHygiene,
		TaskLogMaintenance, TaskServiceCleanup, TaskFullScan,
	} {
		if stepTimeout(step) <= 0 {
			t.Errorf("stepTimeout(%q) = %v, want positive", step, stepTimeout(step))
		}
	}
}

func TestFullScanOrderEndsOnDiskCheck(t *testing.T) {
	// The console's last-step column and the server's per-step accumulator both
	// depend on disk_check being last: it is the terminal report.
	if n := len(fullScanSteps); n != 4 {
		t.Fatalf("fullScanSteps has %d entries, want 4", n)
	}
	if fullScanSteps[len(fullScanSteps)-1] != TaskDiskCheck {
		t.Errorf("fullScanSteps ends on %q, want %q", fullScanSteps[len(fullScanSteps)-1], TaskDiskCheck)
	}
	for _, step := range fullScanSteps {
		if !Allowed(step) {
			t.Errorf("full_scan step %q is not in the allowlist", step)
		}
	}
}

func TestCapabilitiesAdvertisesTheCommandName(t *testing.T) {
	// main.go registers the dispatcher handler under exactly this string.
	caps := Capabilities()
	if len(caps) != 1 || caps[0] != "maintenance.run" {
		t.Errorf("Capabilities() = %v, want [maintenance.run]", caps)
	}
}

func TestTruncateBoundsAgentStrings(t *testing.T) {
	long := make([]byte, 9000)
	for i := range long {
		long[i] = 'a'
	}
	if got := truncate(string(long), 8*1024); len(got) != 8*1024 {
		t.Errorf("truncate left %d bytes, want 8192", len(got))
	}
	if got := truncate("short", 8*1024); got != "short" {
		t.Errorf("truncate mangled a short string: %q", got)
	}
}

// captureServer records every step report the engine posts, then answers 200 so
// the sweep runs to completion.
type captureServer struct {
	*httptest.Server
	paths   []string
	idHdr   []string
	secret  []string
	reports []srv.StepReport
}

func newCaptureServer(t *testing.T) *captureServer {
	t.Helper()
	cs := &captureServer{}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.paths = append(cs.paths, r.URL.Path)
		cs.idHdr = append(cs.idHdr, r.Header.Get("X-Device-Id"))
		cs.secret = append(cs.secret, r.Header.Get("X-Device-Secret"))
		var rep srv.StepReport
		if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
			t.Errorf("decode report: %v", err)
		}
		cs.reports = append(cs.reports, rep)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(cs.Close)
	return cs
}

// engineFor points an Engine at cs without running a real sweep, so a test can
// exercise report() in isolation.
func engineFor(cs *captureServer, deviceID, secret string) *Engine {
	e := NewEngine(cs.URL, deviceID, secret)
	return e
}

// TestReportWireContract pins the one thing the server actually depends on: the
// path and the two header names AuthenticateAgent reads. Neither is checked by
// any other test here, and a rename of either is silent — the engine still
// returns success locally while the server answers 401 for every report in
// production.
func TestReportWireContract(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")

	if err := e.report(t.Context(), StepReport{
		TaskID: "t-7",
		Step:   TaskDiskCheck,
		Status: statusCompleted,
	}); err != nil {
		t.Fatalf("report: %v", err)
	}

	want := "/api/agent/maintenance/tasks/t-7/result"
	if got := cs.paths[0]; got != want {
		t.Errorf("report path = %q, want %q", got, want)
	}
	// These two names are read by devicemgmt.AuthenticateAgent; nothing else
	// connects the agent's client to the server's expectation.
	if got := cs.idHdr[0]; got != "dev-42" {
		t.Errorf("X-Device-Id = %q, want dev-42", got)
	}
	if got := cs.secret[0]; got != "s3cret" {
		t.Errorf("X-Device-Secret = %q, want s3cret", got)
	}
}

// TestFullScanReportsRunningUntilLastStep is the agent-side half of the
// binding contract: the server absorbs any report for a step earlier than the
// one it has recorded, so a full_scan that posts 'completed' after step 1 has
// its remaining three steps discarded and records only step 1's bytes_freed.
// The server's own test cannot catch this, because it drives the repository
// directly and never runs the agent.
//
// This drives the REAL Run loop through the runStep seam. An earlier version
// reimplemented the status choice inside the test, which asserted nothing about
// the production path — it stayed green when `if !last` was broken.
func TestFullScanReportsRunningUntilLastStep(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")

	executed := 0
	e.runStep = func(_ context.Context, step string) (stepOutcome, error) {
		executed++
		return stepOutcome{output: "ok: " + step, exitCode: 0}, nil
	}

	raw := json.RawMessage(`{"task_id":"t-1","task_type":"full_scan"}`)
	if err := e.Run(t.Context(), raw, "t-1"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if executed != len(fullScanSteps) {
		t.Errorf("ran %d steps, want %d", executed, len(fullScanSteps))
	}
	if len(cs.reports) != len(fullScanSteps) {
		t.Fatalf("posted %d reports, want %d", len(cs.reports), len(fullScanSteps))
	}
	for i, step := range fullScanSteps {
		got := cs.reports[i]
		if got.Step != step {
			t.Errorf("report %d step = %q, want %q (order matters: the server drops a step it has already passed)",
				i, got.Step, step)
		}
		want := statusRunning
		if i == len(fullScanSteps)-1 {
			want = srv.TaskStatusCompleted
		}
		if got.Status != want {
			t.Errorf("report %d (%s) status = %q, want %q", i, step, got.Status, want)
		}
	}
}

// TestAMissingTaskIDFallsBackToTheCommandID is the other half of the stranded-row
// fix. The server puts task_id in the payload, but the envelope id is the same
// value; when the payload omits it the report has to go out under the id the
// command arrived with, or the server finds no task to update.
func TestAMissingTaskIDFallsBackToTheCommandID(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")
	e.runStep = func(_ context.Context, step string) (stepOutcome, error) {
		return stepOutcome{output: "ok", exitCode: 0}, nil
	}

	if err := e.Run(t.Context(),
		json.RawMessage(`{"task_type":"memory_hygiene"}`), "env-id-9"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(cs.reports) != 1 {
		t.Fatalf("posted %d reports, want 1", len(cs.reports))
	}
	if got := cs.reports[0].TaskID; got != "env-id-9" {
		t.Errorf("report task_id = %q, want env-id-9", got)
	}
}

// TestAppendLogCarriesAFailedSubprocess is the regression test for the defect
// that made an unelevated disk_check look clean. `chkdsk /scan` on an agent
// without elevation exits non-zero and prints "Access Denied ... You have to
// invoke this utility running in elevated mode", but appendLog used to return
// only the text, so the step reported exit 0 and the operator saw a green sweep
// that had scanned nothing at all.
func TestAppendLogCarriesAFailedSubprocess(t *testing.T) {
	acc := stepOutcome{output: "C:\nD:"}
	acc = appendLog(acc, stepOutcome{
		output:   "Access Denied as you do not have sufficient privileges.",
		exitCode: 2,
	})

	if acc.exitCode == 0 {
		t.Fatal("a failed subprocess was folded in and the step still reports exit 0")
	}
	if !strings.Contains(acc.output, "Access Denied") {
		t.Errorf("transcript lost, output = %q", acc.output)
	}
	if !strings.Contains(acc.output, "D:") {
		t.Errorf("accumulated text lost, output = %q", acc.output)
	}
}

// TestAppendLogKeepsTheFirstFailure guards the merge rule: a later success must
// not paper over an earlier failure, and the first non-zero code is the one the
// operator sees.
func TestAppendLogKeepsTheFirstFailure(t *testing.T) {
	acc := stepOutcome{}
	acc = appendLog(acc, stepOutcome{output: "first", exitCode: 3})
	acc = appendLog(acc, stepOutcome{output: "second", exitCode: 0})

	if acc.exitCode != 3 {
		t.Errorf("exitCode = %d, want 3 (the first failure, not the later success)", acc.exitCode)
	}
	if strings.Count(acc.output, "\n") != 1 {
		t.Errorf("both transcripts should be present, got %q", acc.output)
	}
}

// TestAppendLogSumsBytesFreed keeps the byte total additive across the
// subprocesses folded into one step.
func TestAppendLogSumsBytesFreed(t *testing.T) {
	acc := stepOutcome{}
	acc = appendLog(acc, stepOutcome{output: "a", bytesFreed: 100})
	acc = appendLog(acc, stepOutcome{output: "b", bytesFreed: 250})

	if acc.bytesFreed != 350 {
		t.Errorf("bytesFreed = %d, want 350", acc.bytesFreed)
	}
}

// TestAppendLogSticksARebootRequest covers the one flag that must not be
// cleared by any later subprocess in the same step.
func TestAppendLogSticksARebootRequest(t *testing.T) {
	acc := stepOutcome{}
	acc = appendLog(acc, stepOutcome{output: "a", rebootNeeded: true})
	acc = appendLog(acc, stepOutcome{output: "b"})

	if !acc.rebootNeeded {
		t.Error("rebootNeeded was cleared by a later clean subprocess")
	}
}

// TestAFailedDiskCheckStepIsNotReportedClean covers the half of the unelevated
// disk_check defect that appendLog alone cannot fix. appendLog carries the exit
// code up, but the step handler still returns (stepOutcome, nil): chkdsk and
// defrag print their refusal instead of raising, so nothing in the Go error path
// fires. Without the disk_check branch in Run the step posted "completed" with
// a transcript that said the volume was never scanned.
func TestAFailedDiskCheckStepIsNotReportedClean(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")
	e.runStep = func(_ context.Context, step string) (stepOutcome, error) {
		return stepOutcome{
			output:   "Access Denied as you do not have sufficient privileges.",
			exitCode: 3,
		}, nil
	}

	raw := json.RawMessage(`{"task_id":"t-9","task_type":"disk_check"}`)
	if err := e.Run(t.Context(), raw, "t-9"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(cs.reports) != 1 {
		t.Fatalf("posted %d reports, want 1", len(cs.reports))
	}
	got := cs.reports[0]
	if got.Status != srv.TaskStatusFailed {
		t.Errorf("status = %q, want %q: an unelevated agent scanned nothing "+
			"and the operator must not see a clean disk", got.Status, srv.TaskStatusFailed)
	}
	if got.ExitCode == nil || *got.ExitCode == 0 {
		t.Errorf("exit_code = %v, want a non-zero code", got.ExitCode)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage == "" {
		t.Error("error_message is empty: the reason has to reach the console")
	}
}

// TestAStepThatDeclinesToFailStaysGreen is the guard on the other side of the
// non-zero-exit branch. memory_hygiene and log_maintenance hit a refused
// subprocess on every unprivileged agent, record it in the transcript, and
// deliberately leave their own exit code at zero because the machine was not
// left in a worse state. Reddening them would teach operators to ignore the
// alert, which is what those steps go out of their way to avoid.
func TestAStepThatDeclinesToFailStaysGreen(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")
	e.runStep = func(_ context.Context, step string) (stepOutcome, error) {
		// The real no-op shape: the transcript says it could not act, the
		// step's own code says the step is fine.
		return stepOutcome{
			output:   "EmptyStandbyList.exe not installed; standby list not trimmed (no-op)",
			exitCode: 0,
		}, nil
	}

	raw := json.RawMessage(`{"task_id":"t-10","task_type":"memory_hygiene"}`)
	if err := e.Run(t.Context(), raw, "t-10"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(cs.reports) != 1 {
		t.Fatalf("posted %d reports, want 1", len(cs.reports))
	}
	if got := cs.reports[0].Status; got != srv.TaskStatusCompleted {
		t.Errorf("status = %q, want %q: a step that records a refusal in its "+
			"transcript but does not set a code on itself did the correct thing",
			got, srv.TaskStatusCompleted)
	}
}

// TestCleanupTempRefusedRootsAreNotGreen is the regression test for the false
// green in the screenshot: cleanup_temp logged "scan C:\Windows\Temp: Access is
// denied" and "delete pass on ...: Access is denied" for every root, then
// reported status=completed. A sweep that removed nothing is not a completed
// sweep.
func TestCleanupTempRefusedRootsAreNotGreen(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")
	e.runStep = func(_ context.Context, step string) (stepOutcome, error) {
		// What windowsCleanupTemp now returns on an unelevated agent: the
		// per-root lines carry a non-zero code, and the Go error is still nil.
		return stepOutcome{
			output: "scan C:\\Windows\\Temp: open C:\\Windows\\Temp: Access is denied.\n" +
				"removed 0 files from C:\\Windows\\Temp (age > 24h0m0s)\n" +
				"delete pass on C:\\Windows\\Temp: The process cannot access the file " +
				"because it is being used by another process.\n" +
				"3 of 3 cleanup roots were refused; install the agent as a Windows " +
				"Service so it runs elevated",
			exitCode: 1,
		}, nil
	}

	raw := json.RawMessage(`{"task_id":"t-11","task_type":"cleanup_temp"}`)
	if err := e.Run(t.Context(), raw, "t-11"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(cs.reports) != 1 {
		t.Fatalf("posted %d reports, want 1", len(cs.reports))
	}
	got := cs.reports[0]
	if got.Status != srv.TaskStatusFailed {
		t.Errorf("status = %q, want %q: every root was refused and the sweep "+
			"removed nothing", got.Status, srv.TaskStatusFailed)
	}
	if got.ExitCode == nil || *got.ExitCode == 0 {
		t.Errorf("exit_code = %v, want non-zero", got.ExitCode)
	}
	if got.ErrorMessage == nil || *got.ErrorMessage == "" {
		t.Error("error_message is empty: the console needs the reason")
	}
}

// TestAFullScanWhoseFirstStepFailsDoesNotEndGreen is the regression test for
// the worst defect in this feature: only the last step posted a terminal
// status, so a full_scan whose first step refused every root still ended
// 'completed' the moment disk_check succeeded — a sweep that freed nothing
// rendered as a clean success.
//
// The verdict has to survive to the final report, because that is the only
// one the server does not absorb (repository.go:567).
func TestAFullScanWhoseFirstStepFailsDoesNotEndGreen(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")

	e.runStep = func(_ context.Context, step string) (stepOutcome, error) {
		if step == TaskCleanupTemp {
			// Unelevated agent: every root refused, code 1, no Go error.
			return stepOutcome{output: "all 3 cleanup roots were refused", exitCode: 1}, nil
		}
		return stepOutcome{output: "ok: " + step, exitCode: 0, bytesFreed: 7}, nil
	}

	raw := json.RawMessage(`{"task_id":"t-12","task_type":"full_scan"}`)
	if err := e.Run(t.Context(), raw, "t-12"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(cs.reports) != len(fullScanSteps) {
		t.Fatalf("posted %d reports, want %d", len(cs.reports), len(fullScanSteps))
	}

	// The intermediate reports still carry 'running': they must, or the
	// server closes the task early and drops the steps that follow.
	for i := 0; i < len(fullScanSteps)-1; i++ {
		if cs.reports[i].Status != srv.TaskStatusRunning {
			t.Errorf("report %d (%s) status = %q, want %q",
				i, fullScanSteps[i], cs.reports[i].Status, srv.TaskStatusRunning)
		}
	}

	final := cs.reports[len(cs.reports)-1]
	if final.Status != srv.TaskStatusFailed {
		t.Errorf("final status = %q, want %q: step 1 refused every root, so the "+
			"run failed even though the last step succeeded",
			final.Status, srv.TaskStatusFailed)
	}
	if final.ExitCode == nil || *final.ExitCode != 1 {
		t.Errorf("final exit_code = %v, want 1 (the code of the step that failed, "+
			"not the zero of the step that succeeded last)", final.ExitCode)
	}
	if final.ErrorMessage == nil || *final.ErrorMessage == "" {
		t.Error("final error_message is empty: the console needs the reason")
	}

	// All four steps still reported, so their bytes_freed still accumulate.
	for i, step := range fullScanSteps {
		if cs.reports[i].Step != step {
			t.Errorf("report %d step = %q, want %q", i, cs.reports[i].Step, step)
		}
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func setModTime(t *testing.T, path string, tm time.Time) {
	t.Helper()
	if err := os.Chtimes(path, tm, tm); err != nil {
		t.Fatal(err)
	}
}

func jsonTags(t reflect.Type) map[string]string {
	out := make(map[string]string, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		out[f.Name] = f.Tag.Get("json")
	}
	return out
}

func TestAppendSecondaryLogDoesNotFailStep(t *testing.T) {
	acc := stepOutcome{
		output:     "primary cleanup completed: freed 100MB",
		exitCode:   0,
		bytesFreed: 104857600,
	}

	secTimeout := stepOutcome{
		output:   "command exceeded the 1m30s limit and was stopped",
		exitCode: 1,
		timedOut: true,
	}

	res := appendSecondaryLog(acc, secTimeout, "Clear-RecycleBin")

	if res.exitCode != 0 {
		t.Errorf("appendSecondaryLog set exitCode = %d, want 0", res.exitCode)
	}
	if res.timedOut {
		t.Errorf("appendSecondaryLog set timedOut = true, want false")
	}
	if !strings.Contains(res.output, "warning: Clear-RecycleBin timed out") {
		t.Errorf("appendSecondaryLog output missing warning note, got %q", res.output)
	}
	if res.bytesFreed != 104857600 {
		t.Errorf("bytesFreed = %d, want 104857600", res.bytesFreed)
	}
}

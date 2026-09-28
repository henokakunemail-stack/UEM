package maintenance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
		TaskLogMaintenance, TaskServiceCleanup, TaskFullScan,
	} {
		if !Allowed(good) {
			t.Errorf("allowlist rejected valid task type %q", good)
		}
	}
	if len(allowed) != 6 {
		t.Errorf("allowlist size = %d, want 6", len(allowed))
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
		if err := e.Run(t.Context(), json.RawMessage(raw)); err == nil {
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
	if err := e.Run(t.Context(), json.RawMessage(`{"task_id":"t1","task_type":"disk_check"}`)); err == nil {
		t.Fatal("second concurrent maintenance task was not rejected")
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
	if err := e.Run(t.Context(), raw); err != nil {
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

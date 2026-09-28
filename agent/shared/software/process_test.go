package software

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The helper tests re-enter this test binary as a child process. Go runs them
// here too, so each checks its mode first and returns immediately when it was
// not started as a helper. The mode arrives as a positional argument rather
// than an environment variable: runProcess builds its own exec.Cmd, so an Env
// set on a Cmd the test built would never reach the child.
func helperMode() string { return flag.Arg(0) }

func helperSelf(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate the test binary: %v", err)
	}
	return self
}

func helperArgs(name, mode string) []string {
	return []string{"-test.run=^" + name + "$", mode}
}

// TestHelperNoop exists so the helper-mode dispatch has a target on every
// platform; TestHelperOutput and TestHelperSleep do the real work.
func TestHelperNoop(t *testing.T) {
	if helperMode() == "" {
		return
	}
	os.Exit(0)
}

// TestHelperOutput writes far more than the cap so the parent can check both
// that output is captured and that it is bounded.
func TestHelperOutput(t *testing.T) {
	if helperMode() == "" {
		return
	}
	line := strings.Repeat("x", 200)
	for i := 0; i < 20000; i++ {
		os.Stdout.WriteString(line + "\n")
	}
	os.Exit(0)
}

// TestHelperSleep never exits on its own. The parent's deadline is the only
// thing that ends it, which is the property under test. When its argument is a
// path it records its own pid there first, so the parent can check that
// specific process is gone rather than counting processes by name.
func TestHelperSleep(t *testing.T) {
	mode := helperMode()
	if mode == "" {
		return
	}
	if !strings.HasSuffix(mode, ".pid") {
		time.Sleep(24 * time.Hour)
		os.Exit(0)
	}
	_ = os.WriteFile(mode, []byte(strconv.Itoa(os.Getpid())), 0o644)
	time.Sleep(24 * time.Hour)
	os.Exit(0)
}

// TestCappedWriterKeepsHead checks the memory bound. The cap is the whole point
// of replacing CombinedOutput, so it needs a number attached: a chatty
// installer must cost the same memory as a quiet one.
func TestCappedWriterKeepsHead(t *testing.T) {
	w := &cappedWriter{limit: 64}
	for i := 0; i < 100; i++ {
		if n, err := w.Write([]byte("0123456789")); err != nil || n != 10 {
			t.Fatalf("Write = %d, %v; exec treats a short write as a failed process", n, err)
		}
	}
	s := w.String()
	if !strings.HasPrefix(s, "0123456789") {
		t.Fatalf("captured text does not start at the first byte: %.40q", s)
	}
	if !strings.Contains(s, "truncated") {
		t.Fatalf("truncation is not reported: %q", s)
	}
	if !strings.Contains(s, "64 of 1000 bytes captured") {
		t.Fatalf("kept and dropped bytes are not both reported: %q", s)
	}
	if got := w.buf.Len(); got != 64 {
		t.Fatalf("buffer held %d bytes, want the 64-byte cap", got)
	}
}

func TestCappedWriterUnderCap(t *testing.T) {
	w := &cappedWriter{limit: 1 << 20}
	for i := 0; i < 10; i++ {
		_, _ = w.Write([]byte("msiexec says hello"))
	}
	if s := w.String(); strings.Contains(s, "truncated") {
		t.Fatalf("reported truncation below the cap: %q", s)
	}
}

// TestRunProcessTimeoutKillsTree checks the three things a timeout has to do:
// fire, be reported as a timeout rather than a generic failure, and leave
// nothing running. A task that times out and leaves an installer behind has
// not been fixed, it has been moved.
func TestRunProcessTimeoutKillsTree(t *testing.T) {
	self := helperSelf(t)
	pidFile := t.TempDir() + "/sleeper.pid"

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	res := runProcess(ctx, self, helperArgs("TestHelperSleep", pidFile))
	elapsed := time.Since(start)

	if !errors.Is(res.err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", res.err)
	}
	if res.exitCode != -1 {
		t.Fatalf("exit code = %d, want -1 for a process we killed", res.exitCode)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("returned after %s; the grace and kill waits should be seconds, not minutes", elapsed)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("helper never recorded its pid (%v); the timeout may have fired before the child started", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("helper recorded %q, which is not a pid", raw)
	}
	if processAlive(pid) {
		t.Fatalf("pid %d is still running after runProcess returned; the installer outlived the task", pid)
	}
}

// TestRunProcessCollectsBoundedOutput is the ordinary path: the process runs,
// its output is captured and bounded, and its exit code passes through
// unchanged. An installer that exits 3010 is a success to the caller, so losing
// that number would silently break MSI handling.
func TestRunProcessCollectsBoundedOutput(t *testing.T) {
	self := helperSelf(t)
	res := runProcess(context.Background(), self, helperArgs("TestHelperOutput", "go"))

	if res.err != nil {
		t.Fatalf("err = %v, want nil", res.err)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", res.exitCode)
	}
	if !strings.Contains(res.output, "xxx") {
		t.Fatalf("output was not captured: %.80q", res.output)
	}
	if len(res.output) > maxCapturedOutput+256 {
		t.Fatalf("captured %d bytes, want at most the %d-byte cap plus the truncation note",
			len(res.output), maxCapturedOutput)
	}
	if !strings.Contains(res.output, "truncated") {
		t.Fatal("a 4 MB installer produced no truncation notice; the cap is not in the path")
	}
}

// TestQueueSerializes is the property the whole queue exists for: one at a
// time, and in order.
func TestQueueSerializes(t *testing.T) {
	q := NewTaskQueue()

	var (
		mu      sync.Mutex
		active  int
		maxSeen int
		order   []int
	)
	for i := 0; i < 3; i++ {
		i := i
		if err := q.Submit("task-"+string(rune('a'+i)), "install", func() {
			mu.Lock()
			active++
			if active > maxSeen {
				maxSeen = active
			}
			order = append(order, i)
			active--
			mu.Unlock()
		}); err != nil {
			t.Fatalf("Submit %d = %v; the queue rejected a burst it should hold", i, err)
		}
	}

	// Close drains the queue, so by the time it returns every task has run.
	q.Close()

	if maxSeen != 1 {
		t.Fatalf("%d tasks ran at once, want 1", maxSeen)
	}
	if len(order) != 3 || order[0] != 0 || order[2] != 2 {
		t.Fatalf("tasks ran out of order: %v", order)
	}
}

func TestQueueRejectsPastDepth(t *testing.T) {
	q := NewTaskQueue()
	defer q.Close()

	release := make(chan struct{})
	for i := 0; i < queueDepth+1; i++ {
		if err := q.Submit("t", "install", func() { <-release }); err != nil {
			t.Fatalf("Submit %d = %v, want nil", i, err)
		}
	}
	err := q.Submit("overflow", "install", func() {})
	if err == nil {
		t.Fatal("accepted a task past the limit; the server would wait forever for a task that never runs")
	}
	if !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("rejection message does not name the task: %v", err)
	}
	close(release)
}

func TestQueueSurvivesPanic(t *testing.T) {
	q := NewTaskQueue()
	defer q.Close()

	if err := q.Submit("boom", "install", func() { panic("installer bug") }); err != nil {
		t.Fatalf("Submit = %v", err)
	}
	done := make(chan struct{})
	if err := q.Submit("after", "install", func() { close(done) }); err != nil {
		t.Fatalf("Submit after panic = %v", err)
	}
	q.Close()
	select {
	case <-done:
	default:
		t.Fatal("the worker died with the panicking task; every later task on this device is now a no-op")
	}
}

// processAlive reports whether a pid is still running.
//
// Signal 0 is the portable liveness probe on Unix. On Windows os.Process.Signal
// only implements Kill, so the check goes through tasklist, which answers
// "no tasks match" for a pid that has exited and been reaped.
func processAlive(pid int) bool {
	if runtime.GOOS == "windows" {
		out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
		if err != nil {
			return true // cannot prove it died; assume the worse
		}
		return strings.Contains(string(out), strconv.Itoa(pid))
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// A process that exists but cannot be signalled is still alive.
	return p.Signal(syscall.Signal(0)) == nil
}

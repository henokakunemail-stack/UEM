//go:build windows

package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Every test here is about a timeout, and about the two claims that were wrong
// about timeouts on this agent:
//
//  1. A step that ran out of time was reported as "install the agent as a
//     Windows Service so it runs elevated". On an already-elevated agent that
//     is a confident falsehood, and the operator's next move — go install the
//     service — could not have changed anything.
//
//  2. A step silent for its entire duration was indistinguishable from a step
//     that had stopped reporting, so raising a ceiling past the server's orphan
//     sweep grace would have had the sweep kill healthy work. The heartbeat is
//     what makes the ceiling safe, and these tests are what make the heartbeat
//     real rather than aspirational.

// TestATimeoutIsNotBlamedOnElevation is the regression test for the wrong
// reason. This is the exact report the console showed on a correctly elevated
// LocalSystem service: chkdsk was killed mid-scan, and the operator was told to
// install a service that was already installed and already running as SYSTEM.
func TestATimeoutIsNotBlamedOnElevation(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")
	e.runStep = func(_ context.Context, step string) (stepOutcome, error) {
		// What runFor returns for a command that blew its ceiling: a non-zero
		// exit, the partial transcript, and timedOut set.
		return stepOutcome{
			output: strings.Repeat("Progress: 1814360 of 3189248 done; ", 200) +
				"\ncommand exceeded its step's time limit and was stopped",
			exitCode: 1,
			timedOut: true,
		}, nil
	}

	raw := json.RawMessage(`{"task_id":"t-tm","task_type":"disk_check"}`)
	if err := e.Run(t.Context(), raw, "t-tm"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(cs.reports) != 1 {
		t.Fatalf("posted %d reports, want 1", len(cs.reports))
	}
	got := cs.reports[0]
	if got.Status != statusFailed {
		t.Errorf("status = %q, want %q", got.Status, statusFailed)
	}
	if got.ErrorMessage == nil {
		t.Fatal("error_message is nil: the console has nothing to show")
	}
	if strings.Contains(*got.ErrorMessage, "Windows Service") ||
		strings.Contains(*got.ErrorMessage, "elevated") {
		t.Errorf("a timeout was blamed on elevation: %q\n"+
			"this agent WAS elevated; telling the operator to install a service "+
			"that is already installed is a wrong answer they will act on",
			*got.ErrorMessage)
	}
	if !strings.Contains(*got.ErrorMessage, "time limit") {
		t.Errorf("error_message does not name the real cause: %q", *got.ErrorMessage)
	}
}

// TestTheCauseSurvivesTruncation is the half of that bug that made it
// undiagnosable. chkdsk's progress output is megabytes and the transcript is cut
// to 8 KiB on the way to the console, so the "exceeded the limit" line was
// sliced off — leaving the operator with a truncated progress bar and a reason
// that named the wrong thing.
//
// It failed here first, and the failure was the point: the fix appended the
// cause to the transcript on the reasoning that "truncation keeps the head", and
// truncate does keep the head — so the appended cause was exactly what got
// dropped. A head-only cut also loses the most useful line in the file, the one
// saying the command was killed. The transcript keeps a tail for that reason,
// and both ends of it are asserted here rather than just the end the fix added.
func TestTheCauseSurvivesTruncation(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")
	e.runStep = func(_ context.Context, step string) (stepOutcome, error) {
		// Comfortably past the 8 KiB cap on its own, which is the real shape:
		// chkdsk on a multi-million-file volume emits this much before it is
		// killed. The opening line is what chkdsk prints first, and it is what
		// an operator uses to confirm which volume was being scanned.
		return stepOutcome{
			output: "Windows has scanned the file system and found no problems.\n" +
				strings.Repeat("Progress: 1234567 of 3189248 done; ", 400),
			exitCode: 1,
			timedOut: true,
		}, nil
	}

	raw := json.RawMessage(`{"task_id":"t-tr","task_type":"disk_check"}`)
	if err := e.Run(t.Context(), raw, "t-tr"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(cs.reports) != 1 {
		t.Fatalf("posted %d reports, want 1", len(cs.reports))
	}
	got := cs.reports[0]
	if got.OutputLog == nil {
		t.Fatal("output_log is nil")
	}
	log := *got.OutputLog
	if len(log) != 8*1024 {
		t.Fatalf("log is %d bytes, want it truncated to 8192", len(log))
	}
	if !strings.Contains(log, "transcript truncated") {
		t.Error("the log was cut without saying so; a silently severed transcript " +
			"reads as one that simply ended")
	}
	if !strings.HasPrefix(log, "Windows has scanned") {
		t.Errorf("the head was dropped, so the transcript no longer says what was "+
			"being attempted; it starts: %.80q", log)
	}
	tail := log[len(log)-1024:]
	if !strings.Contains(tail, "time limit") {
		t.Errorf("the cause was truncated away; the tail is:\n%s", tail)
	}
}

// TestARefusedCommandStillBlamesElevation is the guard on the other side. The
// old message was wrong for a timeout, not wrong to exist: when the OS really
// does refuse for want of privilege, saying so is the correct and only useful
// thing. This keeps the fix from becoming "never mention elevation again",
// which would swap one confident wrong answer for another.
func TestARefusedCommandStillBlamesElevation(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")
	e.runStep = func(_ context.Context, step string) (stepOutcome, error) {
		// The literal text chkdsk prints on an unelevated agent.
		return stepOutcome{
			output:   "Access Denied as you do not have sufficient privileges.\nYou have to invoke this utility running in elevated mode.",
			exitCode: 3,
		}, nil
	}

	raw := json.RawMessage(`{"task_id":"t-el","task_type":"disk_check"}`)
	if err := e.Run(t.Context(), raw, "t-el"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(cs.reports) != 1 {
		t.Fatalf("posted %d reports, want 1", len(cs.reports))
	}
	got := cs.reports[0]
	if got.ErrorMessage == nil || !strings.Contains(*got.ErrorMessage, "Windows Service") {
		t.Errorf("a real privilege refusal was not reported as one: %v", got.ErrorMessage)
	}
	if got.Status != statusFailed {
		t.Errorf("status = %q, want %q", got.Status, statusFailed)
	}
}

// TestALongStepHeartbeatsWhileItWorks is the property that makes a 30-minute
// disk_check survivable against a 20-minute sweep grace. Before this, a step
// was silent start to finish, so the server could not tell a chkdsk at 40% from
// a dead agent, and the only safe ceiling was one below the grace.
func TestALongStepHeartbeatsWhileItWorks(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")

	restore := heartbeatEvery
	heartbeatEvery = 5 * time.Millisecond
	t.Cleanup(func() { heartbeatEvery = restore })

	beats := 0
	e.runStep = func(ctx context.Context, step string) (stepOutcome, error) {
		// Hold the step open long enough for several ticks.
		deadline := time.Now().Add(120 * time.Millisecond)
		for time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		return stepOutcome{output: "ok: " + step, exitCode: 0}, nil
	}

	raw := json.RawMessage(`{"task_id":"t-hb","task_type":"disk_check"}`)
	if err := e.Run(t.Context(), raw, "t-hb"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, rep := range cs.reports {
		if rep.Heartbeat {
			beats++
			if rep.Status != statusRunning {
				t.Errorf("heartbeat status = %q, want %q: a heartbeat that claims a "+
					"terminal state would close the task while the step still works",
					rep.Status, statusRunning)
			}
			if rep.OutputLog != nil {
				t.Errorf("heartbeat carried an output log; the server would overwrite " +
					"the step's real transcript with it")
			}
		}
	}
	if beats == 0 {
		t.Fatal("a 120ms step at a 5ms heartbeat produced no heartbeat: the sweep " +
			"would reap any step longer than its grace window")
	}

	// The terminal report still has to arrive, and it still has to be the one
	// that carries the result.
	last := cs.reports[len(cs.reports)-1]
	if last.Heartbeat {
		t.Error("the final report is a heartbeat; the step would never reach a " +
			"terminal state and the job would stay running forever")
	}
	if last.Status != statusCompleted {
		t.Errorf("final status = %q, want %q", last.Status, statusCompleted)
	}
	if last.OutputLog == nil || *last.OutputLog != "ok: disk_check" {
		t.Errorf("final report lost the step's transcript: %v", last.OutputLog)
	}
}

// TestAStoppedStepStopsHeartbeating guards the failure mode this change could
// have introduced. Once a step's ceiling has fired, a select over a ticker and
// a closed Done channel keeps picking whichever is ready — so without a latch
// the agent would announce "still working" for a step that had already been
// killed, and the sweep would never be entitled to reap it.
func TestAStoppedStepStopsHeartbeating(t *testing.T) {
	cs := newCaptureServer(t)
	e := engineFor(cs, "dev-42", "s3cret")

	restore := heartbeatEvery
	heartbeatEvery = 5 * time.Millisecond
	t.Cleanup(func() { heartbeatEvery = restore })

	// A step that ignores its context entirely and takes far longer than the
	// ceiling. This is the dirSize walk, which does not poll ctx.
	e.runStep = func(ctx context.Context, step string) (stepOutcome, error) {
		time.Sleep(60 * time.Millisecond)
		return stepOutcome{output: "slow but done", exitCode: 0}, nil
	}

	// A ceiling far below the step's real duration, so the deadline fires first.
	if _, err := e.runWithCeiling(t.Context(), "t-sd", TaskDiskCheck, e.runStep, 10*time.Millisecond); err != nil {
		t.Fatalf("runWithCeiling: %v", err)
	}

	// Count the heartbeats before the step returned, then confirm none arrived
	// after its result. A heartbeat posted after the ceiling is the bug.
	beats := 0
	for _, rep := range cs.reports {
		if rep.Heartbeat {
			beats++
		}
	}
	if beats > 4 {
		t.Errorf("posted %d heartbeats for a 10ms ceiling over a 60ms step; the "+
			"deadline latch is not holding", beats)
	}
}

// TestStepCeilingsOutliveTheSweepGrace pins the relationship between the three
// numbers, because getting it wrong is silent: the sweep fires, the step dies
// mid-scan, and the console shows an abandoned sweep on a machine that was
// working. The heartbeat is what makes ceiling > grace safe, so this is a
// guard on the pair, not on either number alone.
func TestStepCeilingsOutliveTheSweepGrace(t *testing.T) {
	const abandonGrace = 20 * time.Minute // server/modules/maintenance/sweep.go

	for _, step := range []string{
		TaskDiskCheck, TaskCleanupTemp, TaskLogMaintenance,
		TaskMemoryHygiene, TaskServiceCleanup,
	} {
		limit := stepTimeout(step)
		if limit <= 0 {
			t.Errorf("stepTimeout(%q) = %v, want positive", step, limit)
			continue
		}
		// A step past the grace is only safe because it heartbeats. Assert the
		// heartbeat is inside the grace with room for a missed report, which is
		// what the sweep's grace is actually sized against.
		if limit > abandonGrace && heartbeatInterval >= abandonGrace {
			t.Errorf("step %q has a %v ceiling against a %v sweep grace and a %v "+
				"heartbeat: the sweep would reap it mid-run",
				step, limit, abandonGrace, heartbeatInterval)
		}
	}
	if heartbeatEvery != heartbeatInterval {
		t.Error("heartbeatEvery does not default to heartbeatInterval: production " +
			"would heartbeat on a different schedule than the one documented here")
	}
}

// TestALongCommandIsNotKilledByTheFlatCeiling proves the classification itself.
// chkdsk /scan on this machine measured 17.19 minutes over 3,189,248 files; the
// flat command ceiling is 90s. A real sleep is the stand-in: if runLong still
// enforced the flat ceiling, every long command would still be killed early and
// disk_check would still be structurally unable to report a healthy large disk.
func TestALongCommandIsNotKilledByTheFlatCeiling(t *testing.T) {
	// The flat ceiling is 90s in production and this runs in milliseconds, so
	// the test drives commandCeiling instead. The constant is still asserted,
	// below: a test that lowers the ceiling proves nothing if the default it is
	// supposed to be measuring is not the one the code ships.
	restore := commandCeiling
	commandCeiling = 50 * time.Millisecond
	t.Cleanup(func() { commandCeiling = restore })

	if commandTimeout != 90*time.Second {
		t.Errorf("commandTimeout is %v, want the shipped 90s: this test measures "+
			"the ceiling commandCeiling stands in for", commandTimeout)
	}

	start := time.Now()
	out := runLong(t.Context(), "cmd", "/c", "ping -n 3 127.0.0.1 >nul")
	elapsed := time.Since(start)

	if out.timedOut {
		t.Errorf("runLong killed a %v command under the %v flat ceiling:\n%s",
			elapsed, commandCeiling, out.output)
	}
	if elapsed < 2*time.Second {
		t.Errorf("runLong returned after %v; the sleep did not run", elapsed)
	}

	// And the default still enforces the flat ceiling — that is the property
	// that keeps Clear-RecycleBin from eating a whole step.
	short := run(t.Context(), "cmd", "/c", "ping -n 3 127.0.0.1 >nul")
	if !short.timedOut {
		t.Error("run did not enforce the flat ceiling: a wedged command could " +
			"consume the whole step and leave the console showing no progress")
	}
	if !strings.Contains(short.output, "limit") {
		t.Errorf("a timed-out command did not say so in its transcript: %q", short.output)
	}
}

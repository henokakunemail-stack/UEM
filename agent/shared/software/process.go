package software

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// ErrTimeout marks a run that was killed for exceeding its deadline. The
// console has to be able to say "this installer hung" separately from "this
// installer exited 1603", because they call for different operator actions:
// one is a bad package, the other is a bad switch.
var ErrTimeout = errors.New("process exceeded its time limit and was terminated")

const (
	// maxCapturedOutput bounds what one task keeps in memory.
	//
	// The old runners called cmd.CombinedOutput(), which buffers everything an
	// installer ever writes. A chatty installer — a Java-based one can emit a
	// line per file, a failed MSI dumps its whole log — grows that buffer
	// without limit inside an agent that is itself a service with no console to
	// notice a balloon. 1 MiB is far more than any install log an operator
	// reads in the View Logs box, and the excess is counted and reported rather
	// than dropped silently, so "truncated" is visible instead of looking like
	// a suspiciously short install.
	maxCapturedOutput = 1 << 20

	// killGrace is how long a cancelled tree gets to exit on its own before the
	// platform tree kill is issued. Installers flush MSI logs on the way out;
	// killing them mid-flush costs the operator the diagnostic that explains
	// the failure.
	killGrace = 5 * time.Second

	// waitDelay is how long cmd.Wait tolerates a surviving descendant holding
	// the stdout pipe. Without it a leaked handle turns a killed task into a
	// hung agent: Wait blocks on the pipe read, not on the process, forever.
	waitDelay = 2 * time.Second
)

// runResult is what one process execution produced.
type runResult struct {
	exitCode int
	output   string
	err      error
}

// runProcess starts one installer, bounds its output, and guarantees the whole
// process tree is gone when it returns.
//
// Three defects in the old runners are fixed here at once:
//
//   - No timeout. A GUI installer blocked on a human left the task in
//     'installing' forever. ctx carries a deadline; hitting it kills the tree.
//   - No tree kill. Killing a parent does not kill its children on any of the
//     three targets, so an installer outlived the agent that started it.
//   - No output bound. CombinedOutput buffered everything; capture is capped.
//
// The per-OS pieces are processTree.prepare, .attach and .kill, which put the
// child in whatever container the platform provides for giving a process tree a
// lifetime and then end that whole container at once.
func runProcess(ctx context.Context, name string, args []string) runResult {
	// exec.Command, not exec.CommandContext: CommandContext hardwires Cancel to
	// a bare Process.Kill of the parent, which is the thing that does not reach
	// children. The deadline is enforced below so the grace period and the tree
	// kill can run first.
	cmd := exec.Command(name, args...)
	cmd.WaitDelay = waitDelay

	// One writer for both streams. decodeOutput downstream has to see the byte
	// stream the console would, and merging is what CombinedOutput did; keeping
	// them separate only makes the interleaving non-deterministic.
	capture := &cappedWriter{limit: maxCapturedOutput}
	cmd.Stdout = capture
	cmd.Stderr = capture

	// On Windows this does two separate jobs: the creation flags suppress the
	// console window a GUI installer would otherwise flash on the endpoint's
	// desktop, and the job object is what gives the tree a lifetime the timeout
	// can end. Everywhere else it buys the process group.
	tree, err := newProcessTree()
	if err != nil {
		return runResult{exitCode: -1, err: fmt.Errorf("prepare %s: %w", name, err)}
	}
	tree.prepare(cmd)
	// Releasing the tree kills anything still in it, so this must not run until
	// the tree has been dealt with. It doubles as the backstop for the case
	// where the tree refuses to die during the grace period.
	defer tree.close()

	if err := cmd.Start(); err != nil {
		return runResult{exitCode: -1, err: fmt.Errorf("start %s: %w", name, err)}
	}
	if err := tree.attach(cmd); err != nil {
		// The installer is already running and untracked, which is exactly the
		// state this layer exists to prevent. Kill it rather than hand back a
		// live process nobody holds.
		tree.kill(cmd)
		return runResult{exitCode: -1, err: err}
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	var (
		waitErr  error
		timedOut bool
	)
	select {
	case waitErr = <-waited:
	case <-ctx.Done():
		timedOut = true
		waitErr = terminate(cmd, tree, waited)
	}

	out := capture.String()
	switch {
	case timedOut:
		return runResult{exitCode: -1, output: out, err: fmt.Errorf("%w after %s", ErrTimeout, ctx.Err())}
	case waitErr == nil:
		return runResult{exitCode: 0, output: out}
	}

	// A process we killed ourselves reports a signal rather than an installer
	// exit code, and that case is already handled above as timedOut. Anything
	// left here is the installer speaking for itself, so the exit code is
	// meaningful: 3010 and 1641 for MSI, 1603 for a real failure.
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return runResult{exitCode: exitErr.ExitCode(), output: out, err: waitErr}
	}
	return runResult{exitCode: -1, output: out, err: waitErr}
}

// terminate ends a process tree and collects the wait result.
//
// The first attempt is a cooperative interrupt, which an installer that is
// mid-uninstall can honour and exit cleanly from. os.Interrupt is not
// implemented on Windows, so there the cooperative step is skipped and the tree
// kill follows immediately; on Unix an interrupt is exactly the polite form of
// the signal the tree kill would send anyway, and installers here are shell
// scripts and dpkg/rpm rather than GUI programs, so the wait is short.
func terminate(cmd *exec.Cmd, tree *processTree, waited <-chan error) error {
	if !isWindows {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case err := <-waited:
			return err
		case <-time.After(killGrace):
		}
	}

	tree.kill(cmd)
	select {
	case err := <-waited:
		return err
	case <-time.After(killGrace):
		// The tree refused to die. cmd.Wait still returns once WaitDelay
		// expires, so this goroutine cannot leak; the deferred job-handle close
		// on Windows reaps the rest, and an unkillable-kernel-state process
		// group on Unix is the lesser evil against a wedged agent.
		return nil
	}
}

// cappedWriter accumulates up to limit bytes and counts everything past it.
// The count, not the bytes, is what survives, so a chatty installer costs a
// fixed amount of memory no matter how loud it is.
type cappedWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	written  int
	limit    int
	overflow bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.written += len(p)
	switch room := w.limit - w.buf.Len(); {
	case len(p) <= room:
		w.buf.Write(p)
	case room > 0:
		w.buf.Write(p[:room])
		w.overflow = true
	default:
		w.overflow = true
	}
	// Never report a short write: exec treats an error from Stdout as the
	// reason the child's output failed, which would turn a noisy installer
	// into a reported process failure.
	return len(p), nil
}

func (w *cappedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.buf.String()
	if w.overflow {
		s += fmt.Sprintf("\n[output truncated: %d of %d bytes captured]", w.buf.Len(), w.written)
	}
	return s
}

//go:build linux || darwin

package remoteexec

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// ptyHarness runs one pty session and collects everything it prints.
type ptyHarness struct {
	t      *testing.T
	m      *TerminalManager
	id     string
	mu     sync.Mutex
	out    strings.Builder
	closed chan struct{}
}

func startPTY(t *testing.T, cols, rows uint16) *ptyHarness {
	t.Helper()
	h := &ptyHarness{t: t, m: NewTerminalManager(), id: "pty-test", closed: make(chan struct{})}
	err := h.m.StartPTYSession(h.id, "sh", cols, rows, func(s string) {
		h.mu.Lock()
		h.out.WriteString(s)
		h.mu.Unlock()
	}, func() { close(h.closed) })
	if err != nil {
		t.Fatalf("StartPTYSession: %v", err)
	}
	t.Cleanup(func() { _ = h.m.CloseSession(h.id) })
	return h
}

func (h *ptyHarness) send(s string) {
	h.t.Helper()
	if err := h.m.WriteInput(h.id, s); err != nil {
		h.t.Fatalf("WriteInput(%q): %v", s, err)
	}
}

// waitFor polls the output for text; the marker is computed by the shell so a
// match cannot come from the echo of the command line itself.
func (h *ptyHarness) waitFor(text string, within time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		got := h.out.String()
		h.mu.Unlock()
		if strings.Contains(got, text) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.t.Fatalf("timed out waiting for %q; output so far:\n%s", text, h.out.String())
}

func TestPTYSessionIsARealTerminal(t *testing.T) {
	h := startPTY(t, 100, 40)
	// A pipe answers "not a tty" here; that was the old terminal.
	h.send("tty >/dev/null && echo IS_$((1+1))_TTY\n")
	h.waitFor("IS_2_TTY", 5*time.Second)
}

func TestPTYSessionHasTheRequestedSizeAndFollowsResize(t *testing.T) {
	h := startPTY(t, 100, 40)
	h.send("stty size | sed 's/ /x/'\n")
	h.waitFor("40x100", 5*time.Second)

	if err := h.m.Resize(h.id, 132, 50); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	h.send("stty size | sed 's/ /x/'\n")
	h.waitFor("50x132", 5*time.Second)
}

// Ctrl+C has to reach the foreground job as SIGINT. On a pipe it is just a
// byte, and an operator who started something long had no way to stop it.
func TestPTYSessionDeliversCtrlCToTheForegroundJob(t *testing.T) {
	h := startPTY(t, 80, 24)
	// The proof is time: while "sleep 30" holds the shell, the next command
	// cannot print its (shell-computed) marker. Seeing it within 5 seconds
	// means the sleep was interrupted. A text check is not usable here: the pty
	// echoes the typed command line, and some shells still run the rest of a
	// list after an interrupted command.
	h.send("sleep 30\n")
	time.Sleep(300 * time.Millisecond)
	h.send("\x03")
	h.send("echo AFTER_$((20+1))\n")
	h.waitFor("AFTER_21", 5*time.Second)
}

func TestPTYSessionReportsCloseWhenTheShellExits(t *testing.T) {
	h := startPTY(t, 80, 24)
	h.send("exit\n")
	select {
	case <-h.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("onClose was not called after the shell exited")
	}
	if err := h.m.WriteInput(h.id, "x"); err == nil {
		t.Fatal("WriteInput succeeded on an exited session")
	}
}

func TestResizeRejectsPipeSessionsAndZeroSizes(t *testing.T) {
	m := NewTerminalManager()
	if err := m.Resize("missing", 80, 24); err == nil {
		t.Fatal("Resize on a missing session succeeded")
	}
	h := startPTY(t, 80, 24)
	if err := h.m.Resize(h.id, 0, 24); err == nil {
		t.Fatal("Resize to zero columns succeeded")
	}
}

//go:build !windows

package software

import (
	"os/exec"
	"syscall"
)

const isWindows = false

// processTree gives a Unix process tree a lifetime of its own, the same job the
// Windows Job Object does, using the primitive the kernel already provides: the
// process group.
//
// Setpgid makes the child the leader of a fresh group, so its pgid equals its
// pid and kill(-pid) reaches every process that inherited the group. That
// inheritance is the whole point: an installer that shells out to a helper, or
// an MSI client that starts a service installer, leaves those descendants in
// the group, and one killpg reaps the lot. Without Setpgid the child shares the
// agent's own group, where a negative kill would kill the agent too.
type processTree struct {
	// pgid is the child's process group, equal to its pid. Zero until the
	// process has actually started.
	pgid int
}

// newProcessTree needs no resources: the group is created by the fork, not by
// anything we set up in advance.
func newProcessTree() (*processTree, error) {
	return &processTree{}, nil
}

func (t *processTree) prepare(cmd *exec.Cmd) {
	// Pdeathsig is the strongest guarantee available on Linux and the agent
	// should use it: if the agent itself is killed, the kernel sends SIGKILL to
	// the installer at that instant. Without it an installer outlives the agent
	// that started it, mid-install, on a machine nobody is watching. darwin's
	// syscall.SysProcAttr has no Pdeathsig field, so the compile-time build tag
	// splits the two.
	attrs := &syscall.SysProcAttr{Setpgid: true}
	setDeathSignal(attrs)
	cmd.SysProcAttr = attrs
}

// attach records the process group id. The group already exists by the time the
// child is running; all that is left is to remember the number killpg needs.
func (t *processTree) attach(cmd *exec.Cmd) error {
	t.pgid = cmd.Process.Pid
	return nil
}

// kill sends SIGKILL to the whole group. SIGTERM first would be friendlier, but
// the tree is already past its deadline and dpkg in particular is documented to
// leave a broken package database if interrupted mid-transaction, so a hard kill
// that is guaranteed to land beats a polite one that might be caught.
func (t *processTree) kill(cmd *exec.Cmd) {
	if t.pgid > 0 {
		_ = syscall.Kill(-t.pgid, syscall.SIGKILL)
	}
	// Kill the leader directly too. A process that changed its own group would
	// otherwise be missed by the negative-pid kill, and this is the process Go
	// is actually waiting on.
	_ = cmd.Process.Kill()
}

func (t *processTree) close() {
	t.pgid = 0
}

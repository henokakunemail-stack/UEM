//go:build linux

package software

import "syscall"

// setDeathSignal asks the kernel to kill the installer if the agent dies.
// Pdeathsig fires when the parent *thread* exits, which for Go's runtime means
// an agent that shuts down cleanly also takes its installer with it — which is
// the point: an installer left running with no agent to report it is a task the
// server will never see finish.
func setDeathSignal(attrs *syscall.SysProcAttr) {
	attrs.Pdeathsig = syscall.SIGKILL
}

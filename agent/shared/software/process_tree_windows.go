//go:build windows

package software

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

const isWindows = true

// processTree gives a Windows process tree a lifetime of its own, so the
// installer's timeout can end the whole tree and not just the process Go
// happened to start.
//
// A Job Object is the only mechanism on Windows that does this reliably.
// The alternatives all have a hole:
//   - Process.Kill() on the parent leaves every descendant running.
//   - CREATE_NEW_PROCESS_GROUP plus a Ctrl-Break only reaches processes that
//     share a console, and a silent installer has none.
//   - A recursive taskkill /T is a second walk of a tree that is already
//     changing underneath it, and races with it.
//
// A Job Object is a kernel-maintained membership list: the kernel kills every
// process in the job when the job is closed or terminated, with no enumeration
// and no race. golang.org/x/sys/windows is already a direct dependency of this
// module (agent/shared/service/scm_windows.go uses it), so this adds no library.
type processTree struct {
	job windows.Handle
}

// newProcessTree creates the job and arms it to kill its members when closed.
// KILL_ON_JOB_CLOSE is the flag that makes the handle the safety net: even if
// the agent is killed outright, Windows closes the handle as the process dies
// and the job's members go with it.
func newProcessTree() (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("arm job object: %w", err)
	}
	return &processTree{job: job}, nil
}

// prepare sets the creation attributes the child needs. Two separate jobs here:
//
//   - CREATE_NO_WINDOW stops a console program from flashing a window on the
//     endpoint's desktop. The product rule is that no installer may put an
//     interface in front of a user who is using the machine, and a
//     milliseconds-long console flash is still that.
//   - DETACHED_PROCESS is deliberately NOT set: with a job attached there is no
//     console to share, and a detached child is awkward to reason about on
//     shutdown.
func (t *processTree) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}

// attach adds the started process to the job.
//
// ponytail: there is a microsecond-wide window between CreateProcess returning
// here and this call, in which the child has not been assigned yet. A process
// spawned inside that window is not in the job. The correct fix is
// PROC_THREAD_ATTRIBUTE_JOB_LIST at creation time, which needs either a
// os/exec that exposes the primary thread handle or replacing os/exec on
// Windows; Go exposes neither. In practice the window is shorter than the
// child's own startup, so nothing has run user code yet. If a package is ever
// observed to escape the job, replace os/exec on Windows for this path.
func (t *processTree) attach(cmd *exec.Cmd) error {
	// PROCESS_SET_QUOTA is what AssignProcessToJobObject needs; PROCESS_TERMINATE
	// is not used by the assignment itself but is what the kernel requires
	// alongside it for the process to be controllable through this job.
	handle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return fmt.Errorf("open installer process %d: %w", cmd.Process.Pid, err)
	}
	defer windows.CloseHandle(handle)

	if err := windows.AssignProcessToJobObject(t.job, handle); err != nil {
		return fmt.Errorf("assign installer process %d to job: %w", cmd.Process.Pid, err)
	}
	return nil
}

// kill terminates every process in the tree at once, with the kernel doing the
// enumeration instead of us walking a tree that is changing underneath us.
func (t *processTree) kill(cmd *exec.Cmd) {
	if t.job != 0 {
		_ = windows.TerminateJobObject(t.job, 1)
	}
}

// close releases the job handle. With KILL_ON_JOB_CLOSE this is also the
// backstop that reaps anything the earlier steps missed.
func (t *processTree) close() {
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}

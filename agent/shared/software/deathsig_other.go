//go:build darwin || (!windows && !linux && !darwin)

// setDeathSignal is a no-op off Linux.
//
// darwin's syscall.SysProcAttr has Setpgid and Pgid but no Pdeathsig, so there
// is no kernel-level "kill my children when I die" primitive to ask for. The
// process group still gives the tree a lifetime the agent can end deliberately;
// it just cannot be ended by the agent's own death.
//
// ponytail: a macOS agent that is killed mid-install leaves the installer
// running. The server-side sweep reaps the task row, and the next inventory
// shows the product's real state, so the system self-corrects — but nothing
// stops the orphaned installer itself. If macOS agents ever run unattended
// installs that must be interrupted, the options are a launchd wrapper that
// traps SIGTERM, or a helper process that holds the group and watches its
// parent.
package software

import "syscall"

func setDeathSignal(attrs *syscall.SysProcAttr) {}

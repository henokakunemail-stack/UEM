//go:build !windows

package remotecontrol

import "fmt"

// SpawnSessionWorker is the non-Windows stub. Session 0 is a Windows-only
// desktop; on every other GOOS there is no worker to spawn.
func SpawnSessionWorker(exePath string, credsPath string, cfg SessionConfig) (pid uint32, err error) {
	return 0, fmt.Errorf("session-0 worker is only available on Windows")
}

// KillSessionWorker is the non-Windows stub; nothing to kill here.
func KillSessionWorker(pid uint32) error {
	return fmt.Errorf("session-0 worker is only available on Windows")
}

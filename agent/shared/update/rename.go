package update

import (
	"errors"
	"io/fs"
	"os"
	"time"
)

// Seams for tests.
var (
	renameFile   = os.Rename
	renameSleep  = time.Sleep
	renameDelays = []time.Duration{
		250 * time.Millisecond, 500 * time.Millisecond, time.Second,
		2 * time.Second, 3 * time.Second, 5 * time.Second,
	}
)

// renameWithRetry is os.Rename that waits out a transient lock.
//
// The freshly written update binary is an unknown .exe, so antivirus and
// Windows Search open it to scan it the moment the handle is closed. While
// they hold it, renaming it (or the running binary next to it) fails with
// "Access is denied" or a sharing violation, and the update was reported as
// failed even though trying again a second later would have worked. Only
// those lock-shaped errors are retried, for about 12 seconds in all; anything
// else (missing file, wrong volume) fails straight away.
func renameWithRetry(from, to string) error {
	err := renameFile(from, to)
	for _, d := range renameDelays {
		if err == nil || !isTransientLock(err) {
			return err
		}
		renameSleep(d)
		err = renameFile(from, to)
	}
	return err
}

func isTransientLock(err error) bool {
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	return isSharingViolation(err)
}

package update

import (
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"
)

func stubRename(t *testing.T, results ...error) *int {
	t.Helper()
	calls := 0
	origRename, origSleep := renameFile, renameSleep
	renameFile = func(string, string) error {
		calls++
		if calls <= len(results) {
			return results[calls-1]
		}
		return nil
	}
	renameSleep = func(time.Duration) {}
	t.Cleanup(func() { renameFile, renameSleep = origRename, origSleep })
	return &calls
}

var errDenied = &os.LinkError{Op: "rename", Old: "a", New: "b", Err: fs.ErrPermission}

// The reported failure: antivirus holds the new binary for a moment, the first
// rename gets "Access is denied", and the update used to fail on the spot.
func TestRenameWithRetryWaitsOutATransientLock(t *testing.T) {
	calls := stubRename(t, errDenied, errDenied)
	if err := renameWithRetry("a", "b"); err != nil {
		t.Fatalf("rename should succeed on the third try, got %v", err)
	}
	if *calls != 3 {
		t.Fatalf("calls = %d, want 3", *calls)
	}
}

func TestRenameWithRetryGivesUpOnAPersistentLock(t *testing.T) {
	results := make([]error, 100)
	for i := range results {
		results[i] = errDenied
	}
	calls := stubRename(t, results...)
	if err := renameWithRetry("a", "b"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("want the permission error back, got %v", err)
	}
	if *calls != len(renameDelays)+1 {
		t.Fatalf("calls = %d, want %d", *calls, len(renameDelays)+1)
	}
}

func TestRenameWithRetryDoesNotRetryOtherErrors(t *testing.T) {
	missing := &os.LinkError{Op: "rename", Old: "a", New: "b", Err: fs.ErrNotExist}
	calls := stubRename(t, missing)
	if err := renameWithRetry("a", "b"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
	if *calls != 1 {
		t.Fatalf("a missing file was retried %d times", *calls)
	}
}

//go:build windows

package maintenance

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// These cover the distinction that made cleanup_temp report a red failure after
// a sweep that had already deleted 24 files and freed 25 MB.
//
// A file held open by another process and a file this process may not delete
// both make os.Remove return an error, and the caller used to treat them as the
// same thing. That produced a transcript ending in "install the agent as a
// Windows Service" on a machine where the agent WAS one.

// lockFile opens path with no sharing at all, which is what makes a delete of it
// fail with ERROR_SHARING_VIOLATION rather than succeeding. The handle is held
// until the test ends.
func lockFile(t *testing.T, path string) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		0 /* no FILE_SHARE_* at all */, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("lock %s: %v", path, err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(h) })
}

// The bug itself: a locked file must not be classified as a privilege refusal.
// ERROR_SHARING_VIOLATION is not ERROR_ACCESS_DENIED, and nothing an operator can
// do about elevation changes the outcome -- which is why the transcript must not
// tell them to install the agent as a Service.
func TestLockedFileIsNotAPrivilegeRefusal(t *testing.T) {
	dir := t.TempDir()
	locked := filepath.Join(dir, "held.tmp")
	if err := os.WriteFile(locked, []byte("in use"), 0o644); err != nil {
		t.Fatal(err)
	}
	setModTime(t, locked, time.Now().Add(-48*time.Hour))
	lockFile(t, locked)

	_, err := removeOldFiles(dir, 24*time.Hour, func(string) bool { return true })
	if err == nil {
		t.Fatal("removeOldFiles reported no error, but the locked file could not be deleted")
	}
	if isPermissionDenied(err) {
		t.Errorf("a file held open was classified as a privilege refusal: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "used by another process") {
		t.Logf("note: the raw error does not read as a lock: %v", err)
	}
}

// The guard that must survive the fix. A real permission refusal has to stay a
// refusal, or the change above would have silently disabled the one check that
// tells an operator their agent is installed wrong.
func TestPermissionDeniedIsStillARefusal(t *testing.T) {
	if !isPermissionDenied(windows.ERROR_ACCESS_DENIED) {
		t.Error("ERROR_ACCESS_DENIED must classify as a privilege refusal")
	}
	if isPermissionDenied(windows.ERROR_SHARING_VIOLATION) {
		t.Error("ERROR_SHARING_VIOLATION must not classify as a privilege refusal: " +
			"no amount of elevation lets a process delete a file someone else is holding")
	}
	// fs.ErrPermission is checked first so the non-Windows builds keep their
	// existing behaviour even though this file is Windows-only.
	if !isPermissionDenied(fs.ErrPermission) {
		t.Error("fs.ErrPermission must classify as a privilege refusal")
	}
	if isPermissionDenied(nil) {
		t.Error("a nil error is not a refusal")
	}
}

// The end-to-end shape of the bug, at the level the operator sees: cleanup_temp
// must finish green while reporting that a file was left in place. Before the fix
// this combination was impossible -- the locked file forced exit 1 no matter how
// much the sweep had actually deleted.
func TestCleanupTempStaysGreenWithALockedFileInTheRoot(t *testing.T) {
	root := t.TempDir()
	held := filepath.Join(root, "held.tmp")
	if err := os.WriteFile(held, []byte("in use"), 0o644); err != nil {
		t.Fatal(err)
	}
	setModTime(t, held, time.Now().Add(-48*time.Hour))
	lockFile(t, held)

	// A deletable sibling: the sweep did real work, and that has to be what the
	// status reflects.
	open := filepath.Join(root, "stale.tmp")
	if err := os.WriteFile(open, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	setModTime(t, open, time.Now().Add(-48*time.Hour))

	_, err := removeOldFiles(root, 24*time.Hour, func(string) bool { return true })
	if err == nil {
		t.Skip("the platform let the locked file be deleted; nothing to classify")
	}

	if _, statErr := os.Stat(open); !os.IsNotExist(statErr) {
		t.Error("the deletable sibling should still have been removed")
	}
	if _, statErr := os.Stat(held); statErr != nil {
		t.Errorf("the locked file should have been left in place: %v", statErr)
	}

	if isPermissionDenied(err) {
		t.Error("this sweep would have been reported as failed and told to install " +
			"the agent as a Windows Service, for a file no elevation can delete")
	}
}

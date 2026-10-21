//go:build linux

package patch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRebootPendingOnLinux(t *testing.T) {
	dir := t.TempDir()
	rebootRequiredPath = filepath.Join(dir, "reboot-required")
	debianMarkerPath = filepath.Join(dir, "debian_version")
	t.Cleanup(func() {
		rebootRequiredPath = "/var/run/reboot-required"
		debianMarkerPath = "/etc/debian_version"
	})

	if got := RebootPending(); got != nil {
		t.Fatalf("no markers at all = %v, want unknown (nil)", *got)
	}
	if err := os.WriteFile(debianMarkerPath, []byte("12"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RebootPending(); got == nil || *got {
		t.Fatalf("Debian without reboot-required = %v, want false", got)
	}
	if err := os.WriteFile(rebootRequiredPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RebootPending(); got == nil || !*got {
		t.Fatalf("reboot-required present = %v, want true", got)
	}
}

package systempolicy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

// fakeUnix records what the backend did and fails where told to.
type fakeUnix struct {
	writeErr, removeErr, runErr, adminErr error
	writes, removes                       []string
	runs                                  []string
}

func (f *fakeUnix) WriteFile(path string, _ []byte) error {
	f.writes = append(f.writes, path)
	return f.writeErr
}
func (f *fakeUnix) Remove(path string) error {
	f.removes = append(f.removes, path)
	return f.removeErr
}
func (f *fakeUnix) Run(_ context.Context, name string, args ...string) error {
	f.runs = append(f.runs, name+" "+strings.Join(args, " "))
	return f.runErr
}
func (f *fakeUnix) ProvisionAdmin(context.Context, PolicySettings) error { return f.adminErr }

func resultFor(t *testing.T, results []protocol.SettingResult, key string) protocol.SettingResult {
	t.Helper()
	var found []protocol.SettingResult
	for _, r := range results {
		if r.Setting == key {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d results for %s, want exactly 1: %+v", len(found), key, results)
	}
	return found[0]
}

// TestLinuxUSBWriteFailureReported: the blacklist write's error was discarded
// and the block counted as applied.
func TestLinuxUSBWriteFailureReported(t *testing.T) {
	sys := &fakeUnix{writeErr: errors.New("read-only file system")}
	r := resultFor(t, unixResults(context.Background(), PolicySettings{USBStorageMode: USBStorageBlock}, "linux", sys),
		protocol.SettingUSBStorageMode)
	if r.Outcome != protocol.OutcomeFailed || !strings.Contains(r.Reason, "read-only file system") {
		t.Errorf("USB block with a refused write = %+v, want failed with the reason", r)
	}
}

func TestLinuxUSBBlockAppliesAndUnloadsModules(t *testing.T) {
	sys := &fakeUnix{}
	r := resultFor(t, unixResults(context.Background(), PolicySettings{USBStorageMode: USBStorageBlock}, "linux", sys),
		protocol.SettingUSBStorageMode)
	if r.Outcome != protocol.OutcomeApplied || len(sys.writes) != 1 || len(sys.runs) != 2 {
		t.Errorf("result %+v, writes %v, runs %v", r, sys.writes, sys.runs)
	}
}

func TestLinuxUSBReadOnlyUnsupported(t *testing.T) {
	sys := &fakeUnix{}
	r := resultFor(t, unixResults(context.Background(), PolicySettings{USBStorageMode: USBStorageReadOnly}, "linux", sys),
		protocol.SettingUSBStorageMode)
	if r.Outcome != protocol.OutcomeUnsupported || len(sys.writes) != 0 {
		t.Errorf("read-only on Linux = %+v (writes %v), want unsupported and nothing written", r, sys.writes)
	}
}

// TestLinuxWallpaperLockUnsupported: gsettings run from the service never
// reached the user's session, yet counted as applied.
func TestLinuxWallpaperLockUnsupported(t *testing.T) {
	sys := &fakeUnix{}
	results := unixResults(context.Background(), PolicySettings{
		WallpaperEnabled: true, WallpaperURL: "https://corp/w.jpg", InactivityTimeoutMinutes: 15,
	}, "linux", sys)
	for _, key := range []string{protocol.SettingWallpaperEnabled, protocol.SettingInactivityTimeoutMinutes} {
		if r := resultFor(t, results, key); r.Outcome != protocol.OutcomeUnsupported {
			t.Errorf("%s = %+v, want unsupported", key, r)
		}
	}
	for _, run := range sys.runs {
		if strings.HasPrefix(run, "gsettings") {
			t.Errorf("still ran %q, which cannot reach the user's desktop", run)
		}
	}
}

// TestDarwinUSBUnsupported: macOS has no modprobe; the block was counted as
// applied anyway.
func TestDarwinUSBUnsupported(t *testing.T) {
	sys := &fakeUnix{}
	r := resultFor(t, unixResults(context.Background(), PolicySettings{USBStorageMode: USBStorageBlock}, "darwin", sys),
		protocol.SettingUSBStorageMode)
	if r.Outcome != protocol.OutcomeUnsupported || len(sys.writes) != 0 || len(sys.runs) != 0 {
		t.Errorf("USB block on macOS = %+v (writes %v runs %v), want unsupported, nothing done", r, sys.writes, sys.runs)
	}
}

// TestWindowsOnlySettingsReportedUnsupported: a Windows-only setting that is
// actually set is reported, one left at its default needs nothing; and every
// key is accounted for exactly once.
func TestWindowsOnlySettingsReportedUnsupported(t *testing.T) {
	results := unixResults(context.Background(), PolicySettings{
		USBStorageMode: USBStorageAllow, FirewallEnabled: true, WindowsUpdateMode: WindowsUpdateDefault,
	}, "linux", &fakeUnix{})
	if len(results) != len(protocol.SettingKeys) {
		t.Fatalf("%d results, want one per setting key (%d)", len(results), len(protocol.SettingKeys))
	}
	if r := resultFor(t, results, protocol.SettingFirewallEnabled); r.Outcome != protocol.OutcomeUnsupported {
		t.Errorf("firewall on Linux = %+v, want unsupported", r)
	}
	if r := resultFor(t, results, protocol.SettingDisableTaskMgr); r.Outcome != protocol.OutcomeApplied {
		t.Errorf("an unset Windows-only restriction = %+v, want applied (nothing to do)", r)
	}
	if r := resultFor(t, results, protocol.SettingWindowsUpdateMode); r.Outcome != protocol.OutcomeApplied {
		t.Errorf("default Windows Update mode = %+v, want applied", r)
	}
}

func TestLinuxAdminFailureReported(t *testing.T) {
	sys := &fakeUnix{adminErr: errors.New("useradd: permission denied")}
	r := resultFor(t, unixResults(context.Background(), PolicySettings{
		AdminAccountEnabled: true, AdminAccountUsername: "it", AdminAccountPassword: "x",
	}, "linux", sys), protocol.SettingAdminAccountEnabled)
	if r.Outcome != protocol.OutcomeFailed {
		t.Errorf("admin = %+v, want failed", r)
	}
}

//go:build windows

package systempolicy

import (
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

// fakeKey opens fine and refuses (or accepts) every write. No real registry
// key is touched.
type fakeKey struct{ refuse bool }

func (k fakeKey) err() error {
	if k.refuse {
		return windows.ERROR_ACCESS_DENIED
	}
	return nil
}
func (k fakeKey) SetDWordValue(string, uint32) error  { return k.err() }
func (k fakeKey) SetStringValue(string, string) error { return k.err() }
func (k fakeKey) DeleteValue(string) error            { return k.err() }
func (k fakeKey) Close() error                        { return nil }

// useFakeKeys routes every policy write through fakeKey for one test and
// records gpupdate runs and USB ejects instead of performing them.
func useFakeKeys(t *testing.T, refuse bool) (cmds *[]string) {
	t.Helper()
	origOpen, origExisting, origRun, origEject := openPolicyKey, openExistingKey, runCmd, ejectUSB
	t.Cleanup(func() { openPolicyKey, openExistingKey, runCmd, ejectUSB = origOpen, origExisting, origRun, origEject })
	open := func(registry.Key, string) (policyKey, error) { return fakeKey{refuse: refuse}, nil }
	openPolicyKey, openExistingKey = open, open
	var ran []string
	runCmd = func(name string, _ ...string) error { ran = append(ran, name); return nil }
	ejectUSB = func() { ran = append(ran, "eject") }
	return &ran
}

// TestRegistryFailuresReportedPerSetting: the key opened, and then every value
// write was discarded (_ =), so a refused write still reported the setting as
// applied. These functions now return the refusal.
func TestRegistryFailuresReportedPerSetting(t *testing.T) {
	ran := useFakeKeys(t, true)
	for name, apply := range map[string]func() error{
		"usb read-only":   func() error { return applyUSBStoragePolicy(USBStorageReadOnly) },
		"windows update":  func() error { return applyWindowsUpdate(WindowsUpdateAutoInstall) },
		"screen saver":    func() error { return applyScreenSaverPolicy(10, true) },
		"inactivity lock": func() error { return applyInactivityLock(15) },
	} {
		if err := apply(); err == nil {
			t.Errorf("%s: a refused registry write was reported as success", name)
		}
	}
	restrictions := applyRestrictions(PolicySettings{DisableTaskMgr: true, DisableCMD: true,
		DisableRegistryTools: true, DisableControlPanel: true})
	for _, key := range []string{protocol.SettingDisableTaskMgr, protocol.SettingDisableCMD,
		protocol.SettingDisableRegistryTools, protocol.SettingDisableControlPanel} {
		if restrictions[key] == nil {
			t.Errorf("%s: a refused registry write was reported as success", key)
		}
	}
	// A half-written USB policy must not be pushed live with gpupdate.
	for _, r := range *ran {
		if r == "gpupdate" {
			t.Error("gpupdate ran after the USB policy writes were refused")
		}
	}
}

func TestUSBPolicyAppliesAndRefreshesWhenWritesSucceed(t *testing.T) {
	ran := useFakeKeys(t, false)
	if err := applyUSBStoragePolicy(USBStorageBlock); err != nil {
		t.Fatalf("accepted writes reported as %v", err)
	}
	if len(*ran) != 2 || (*ran)[0] != "gpupdate" || (*ran)[1] != "eject" {
		t.Errorf("side effects = %v, want gpupdate then eject", *ran)
	}
	for _, key := range []string{protocol.SettingDisableTaskMgr, protocol.SettingDisableControlPanel} {
		if err := applyRestrictions(PolicySettings{DisableTaskMgr: true})[key]; err != nil {
			t.Errorf("%s: accepted writes reported as %v", key, err)
		}
	}
}

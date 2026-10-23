//go:build windows

package windows

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// A per-test key under HKCU stands in for the HKLM entry the installer writes,
// so the test needs no elevation and leaves nothing behind.
func testUninstallKey(t *testing.T, create bool, installDir string) string {
	t.Helper()
	path := `Software\EndpointAgentTest\` + t.Name()
	t.Cleanup(func() {
		_ = registry.DeleteKey(registry.CURRENT_USER, path)
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\EndpointAgentTest`)
	})
	if create {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
		if err != nil {
			t.Fatal(err)
		}
		_ = k.SetStringValue("DisplayVersion", "1.5.0")
		_ = k.SetStringValue("DisplayIcon", `"`+filepath.Join(installDir, "endpoint-agent.exe")+`"`)
		_ = k.SetStringValue("UninstallString", `"`+filepath.Join(installDir, "uninstall.exe")+`"`)
		k.Close()
	}
	return path
}

// The installer writes DisplayVersion once; a self-update swaps only the
// binary, so Programs and Features kept showing the install-time version. The
// icon pointed at an executable with no icon resource.
func TestTheControlPanelEntryFollowsTheRunningAgent(t *testing.T) {
	dir := t.TempDir()
	path := testUninstallKey(t, true, dir)

	if err := syncInstallRecord(registry.CURRENT_USER, path, 0, "1.5.8", dir); err != nil {
		t.Fatal(err)
	}

	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if v, _, _ := k.GetStringValue("DisplayVersion"); v != "1.5.8" {
		t.Errorf("DisplayVersion = %q, want the running agent's 1.5.8", v)
	}
	icon := filepath.Join(dir, "endpoint-agent.ico")
	if v, _, _ := k.GetStringValue("DisplayIcon"); v != icon {
		t.Errorf("DisplayIcon = %q, want %q", v, icon)
	}
	b, err := os.ReadFile(icon)
	if err != nil || !bytes.Equal(b, agentIcon) || len(b) < 6 || !bytes.Equal(b[:4], []byte{0, 0, 1, 0}) {
		t.Errorf("icon file not written as an .ico (err %v, %d bytes)", err, len(b))
	}
}

// A machine installed without the setup program has no entry, and the agent
// must not invent one: it would show up in Programs and Features with no
// uninstaller behind it.
func TestNoControlPanelEntryIsCreatedWhereThereWasNone(t *testing.T) {
	path := testUninstallKey(t, false, "")
	if err := syncInstallRecord(registry.CURRENT_USER, path, 0, "1.5.8", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE); err == nil {
		k.Close()
		t.Fatal("an entry was created for an agent the installer never registered")
	}
}

// The entry names its install directory through its uninstaller. An agent run
// from another directory (a test copy on an admin's machine, a second install)
// must not rewrite the entry of the agent that is actually installed there.
func TestAnAgentRunFromElsewhereLeavesTheEntryAlone(t *testing.T) {
	installed := t.TempDir()
	path := testUninstallKey(t, true, installed)
	elsewhere := t.TempDir()

	if err := syncInstallRecord(registry.CURRENT_USER, path, 0, "9.9.0", elsewhere); err != nil {
		t.Fatal(err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if v, _, _ := k.GetStringValue("DisplayVersion"); v != "1.5.0" {
		t.Errorf("DisplayVersion = %q: an agent outside the install directory rewrote it", v)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "endpoint-agent.ico")); err == nil {
		t.Error("an icon was written for an entry that is not this agent's")
	}
}

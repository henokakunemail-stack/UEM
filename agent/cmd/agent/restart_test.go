package main

import (
	"reflect"
	"testing"
)

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// launchd names the job in XPC_SERVICE_NAME; the restart has to address that
// label, because no job is labelled with the bare service name.
func TestMacOSRestartAddressesTheLaunchdLabel(t *testing.T) {
	got := serviceRestartCommand("darwin", envOf(map[string]string{"XPC_SERVICE_NAME": "com.endpoint-mgmt.agent"}), false, "endpoint-agent")
	want := []string{"launchctl", "kickstart", "-k", "system/com.endpoint-mgmt.agent"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("restart command = %v, want %v", got, want)
	}
}

// A Terminal session has XPC_SERVICE_NAME=0. That is not a launchd job, so the
// agent has to re-spawn itself; kickstarting there left nothing running.
func TestMacOSRestartFromAShellReSpawns(t *testing.T) {
	for _, env := range []map[string]string{{"XPC_SERVICE_NAME": "0"}, {}} {
		if got := serviceRestartCommand("darwin", envOf(env), false, "endpoint-agent"); got != nil {
			t.Errorf("env %v: restart command = %v, want nil (re-spawn)", env, got)
		}
	}
}

func TestLinuxAndWindowsRestartThroughTheirServiceManagers(t *testing.T) {
	if got := serviceRestartCommand("linux", envOf(map[string]string{"INVOCATION_ID": "abc"}), false, "endpoint-agent"); !reflect.DeepEqual(got, []string{"systemctl", "restart", "endpoint-agent"}) {
		t.Errorf("linux under systemd = %v", got)
	}
	if got := serviceRestartCommand("linux", envOf(nil), false, "endpoint-agent"); got != nil {
		t.Errorf("linux outside systemd = %v, want nil", got)
	}
	if got := serviceRestartCommand("windows", envOf(nil), true, "endpoint-agent"); len(got) == 0 || got[0] != "cmd.exe" {
		t.Errorf("windows service = %v", got)
	}
	if got := serviceRestartCommand("windows", envOf(nil), false, "endpoint-agent"); got != nil {
		t.Errorf("windows interactive = %v, want nil", got)
	}
}

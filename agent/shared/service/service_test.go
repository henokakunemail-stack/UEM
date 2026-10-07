package service

import (
	"strings"
	"testing"
)

// DefaultConfig is what every packaging path hands NewManager, and the two
// strings it sets are what an operator sees in `sc.exe query` output and in the
// systemd unit's Description. A change here is a change to the service's
// identity, and nothing in the repo would have caught one: this package had no
// tests at all, so the name was as mutable as a local variable.
func TestDefaultConfigNamesTheServiceThePackagingInstalls(t *testing.T) {
	cfg := DefaultConfig([]string{"-server", "http://x"})

	if cfg.Name != "endpoint-agent" {
		t.Errorf("Name = %q, want endpoint-agent: the .deb installs "+
			"endpoint-agent.service and the NSIS installer registers the same "+
			"name, so a change here orphans the unit file", cfg.Name)
	}
	if cfg.DisplayName == "" {
		t.Error("DisplayName is empty; it is what sc.exe reports and what the " +
			"unit Description renders")
	}
	if len(cfg.Arguments) != 2 || cfg.Arguments[0] != "-server" {
		t.Errorf("Arguments = %v, want the extra args passed straight through; "+
			"the service is registered with them in ExecStart", cfg.Arguments)
	}
}

// GetExecutablePath is the path the service is registered to run, so it has to
// be absolute -- a relative path in a systemd unit or in sc.exe binPath resolves
// against the service's working directory, which for systemd is / and for the
// SCM is system32. Either way the service would fail to start and the failure
// reads like a broken agent rather than a broken registration.
func TestGetExecutablePathIsAbsolute(t *testing.T) {
	p, err := GetExecutablePath()
	if err != nil {
		t.Fatalf("GetExecutablePath: %v", err)
	}
	if p == "" {
		t.Fatal("GetExecutablePath returned an empty path")
	}
	// os.Executable is absolute on every supported platform, so this asserts a
	// property the function already has -- which is the point: if a future
	// version resolves through a symlink or a wrapper, a relative result here
	// is what breaks installation, and this is where it is noticed.
	if p[0] != '/' && (len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/')) {
		t.Errorf("GetExecutablePath returned %q, which is not an absolute path; a "+
			"relative path in ExecStart or binPath does not resolve from the "+
			"service's working directory", p)
	}
}

// systemdUnit and windowsBinPath are the two pieces of an Install that can be
// wrong without any API returning an error, so they are what this test guards.
// The properties asserted are the ones documented on the functions, and each
// one fails silently in a way an operator reads as an agent bug.
func TestSystemdUnitCarriesTheArgumentsAndTheRecoverySettings(t *testing.T) {
	cfg := Config{
		DisplayName: "Enterprise Endpoint Management Agent",
		Arguments:   []string{"-server", "https://em.example.com"},
	}
	unit := systemdUnit(cfg, "/opt/endpoint-agent/agent")

	if got := strings.Contains(unit, "ExecStart=/opt/endpoint-agent/agent -server https://em.example.com"); !got {
		t.Errorf("ExecStart must carry the agent's arguments so the service starts "+
			"with the same flags a manual run uses; unit was:\n%s", unit)
	}
	if !strings.Contains(unit, "Restart=always") || !strings.Contains(unit, "RestartSec=5s") {
		t.Errorf("Restart=always with RestartSec is what turns a crash into a "+
			"recovery instead of an agent the console reports offline forever; "+
			"unit was:\n%s", unit)
	}
	if !strings.Contains(unit, "KillMode=process") {
		t.Errorf("KillMode=process keeps systemd from reaping a child the agent "+
			"spawned mid-command; unit was:\n%s", unit)
	}
	if !strings.Contains(unit, "WantedBy=multi-user.target") {
		t.Errorf("WantedBy=multi-user.target is what makes `enable` start the "+
			"service at boot; without it the unit is enabled but inert, and "+
			"nothing in the install path reports that. Unit was:\n%s", unit)
	}
	if !strings.Contains(unit, "Description=Enterprise Endpoint Management Agent") {
		t.Errorf("the Description is what `systemctl cat` shows an operator; "+
			"unit was:\n%s", unit)
	}
}

// An empty Arguments slice must not leave a trailing space on ExecStart, since
// the unit is written verbatim and a trailing space is the kind of thing an
// operator copies into a bug report as "the config looks right".
func TestSystemdUnitWithoutArgumentsHasNoTrailingSpace(t *testing.T) {
	unit := systemdUnit(Config{DisplayName: "Agent"}, "/usr/bin/agent")
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "ExecStart=") && strings.HasSuffix(line, " ") {
			t.Errorf("ExecStart has a trailing space: %q", line)
		}
	}
}

// The quotes in windowsBinPath are the whole risk: sc.exe create accepts an
// unquoted path that contains spaces, registers the service, and then fails to
// start with an error pointing at the agent rather than at the command line.
// A path with spaces is the normal case for a real install, not an edge case.
func TestWindowsBinPathQuotesAPathWithSpaces(t *testing.T) {
	got := windowsBinPath(Config{}, `C:\Program Files\Endpoint Agent\agent.exe`)

	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Fatalf("binPath %q is not quoted; sc.exe would parse the program as "+
			"`C:\\Program` and treat the rest as arguments", got)
	}
	// The path must survive intact inside the quotes, not just be present
	// somewhere in the string.
	if !strings.Contains(got, `"C:\Program Files\Endpoint Agent\agent.exe"`) {
		t.Errorf("binPath %q does not contain the full quoted path", got)
	}
}

// Arguments sit outside the quotes, the way they would on a command line. If
// they landed inside, the SCM would pass them to the agent as one argv entry
// and every flag parse would fail.
func TestWindowsBinPathAppendsArgumentsOutsideTheQuotes(t *testing.T) {
	cfg := Config{Arguments: []string{"-server", "https://em.example.com"}}
	got := windowsBinPath(cfg, `C:\agent.exe`)

	want := `"C:\agent.exe" -server https://em.example.com`
	if got != want {
		t.Errorf("binPath = %q, want %q", got, want)
	}
}

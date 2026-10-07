package service

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

var (
	ErrNotSupported     = errors.New("service management not supported on this OS")
	ErrAlreadyInstalled = errors.New("service is already installed")
	ErrNotInstalled     = errors.New("service is not installed")
)

// Config defines the metadata and parameters for registering the agent as an OS service.
type Config struct {
	Name        string
	DisplayName string
	Description string
	Arguments   []string
}

// Manager controls the OS service lifecycle.
type Manager interface {
	Install() error
	Uninstall() error
	Start() error
	Stop() error
	Status() (string, error)
}

// DefaultConfig returns the standard enterprise service configuration for the agent.
func DefaultConfig(extraArgs []string) Config {
	return Config{
		Name:        "endpoint-agent",
		DisplayName: "Enterprise Endpoint Management Agent",
		Description: "Central fleet management, hardware inventory, patch compliance, and security monitoring daemon.",
		Arguments:   extraArgs,
	}
}

// GetExecutablePath returns the absolute path of the currently executing agent binary.
func GetExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("lookup executable path: %w", err)
	}
	return exe, nil
}

// systemdUnit renders the unit file an Install writes. Extracted so the content
// can be asserted without /etc/systemd/system being writable and without
// systemctl present -- both are true in CI, and the content is what an operator
// reads in `systemctl cat` and what actually governs the service's behaviour.
//
// The properties below are load-bearing and silent when wrong:
//
//   - ExecStart carries the agent's own arguments, so the service starts with
//     the same flags a manual run would use.
//   - Restart=always with RestartSec is what turns a crash into a recovery
//     instead of a dead agent that the console then reports offline forever.
//   - KillMode=process means systemd reaps only the agent, not a child it
//     spawned; killing the whole group would take a subprocess down mid-command
//     and the operator would see a command that never answered.
//   - WantedBy=multi-user.target is what makes `enable` start it at boot.
//     Without it the unit is enabled but inert.
func systemdUnit(cfg Config, exePath string) string {
	execCmd := exePath
	if len(cfg.Arguments) > 0 {
		execCmd += " " + strings.Join(cfg.Arguments, " ")
	}
	return fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=always
RestartSec=5s
LimitNOFILE=65536
KillMode=process

[Install]
WantedBy=multi-user.target
`, cfg.DisplayName, execCmd)
}

// windowsBinPath renders the binPath string sc.exe create consumes, quoted so a
// path containing spaces stays one argument. Extracted for the same reason as
// systemdUnit: `sc.exe create` needs Administrator on a Windows host, so the
// quoting cannot be exercised there.
//
// The quotes are the whole risk. `C:\Program Files\...\agent.exe` unquoted
// becomes the program `C:\Program` with `Files\...\agent.exe` as its first
// argument, and sc.exe accepts that registration -- the service is created,
// reports as installed, and fails to start with an error that points at the
// agent rather than at the command line.
//
// The quotes must be literal, not Go's %q. `%q` renders a Go string literal, so
// every backslash in the path is doubled and `C:\Program Files\agent.exe` is
// registered as `C:\\Program Files\\agent.exe` -- a path that does not exist,
// with the same misleading start failure. Windows command-line quoting only
// requires the surrounding quotes; the backslashes are literal between them.
func windowsBinPath(cfg Config, exePath string) string {
	binPath := "\"" + exePath + "\""
	if len(cfg.Arguments) > 0 {
		binPath += " " + strings.Join(cfg.Arguments, " ")
	}
	return binPath
}

//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type linuxManager struct {
	cfg Config
}

// NewManager returns the Linux systemd service implementation.
func NewManager(cfg Config) (Manager, error) {
	if cfg.Name == "" {
		cfg.Name = "endpoint-agent"
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = "Enterprise Endpoint Management Agent"
	}
	return &linuxManager{cfg: cfg}, nil
}

func (m *linuxManager) unitPath() string {
	return filepath.Join("/etc/systemd/system", m.cfg.Name+".service")
}

func (m *linuxManager) Install() error {
	exePath, err := GetExecutablePath()
	if err != nil {
		return err
	}

	unitContent := systemdUnit(m.cfg, exePath)

	if err := os.WriteFile(m.unitPath(), []byte(unitContent), 0644); err != nil {
		return fmt.Errorf("write systemd unit: %w (ensure root/sudo)", err)
	}

	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %s (%w)", strings.TrimSpace(string(out)), err)
	}

	if out, err := exec.Command("systemctl", "enable", m.cfg.Name).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable: %s (%w)", strings.TrimSpace(string(out)), err)
	}

	return nil
}

func (m *linuxManager) Uninstall() error {
	_ = m.Stop()
	_ = exec.Command("systemctl", "disable", m.cfg.Name).Run()

	if err := os.Remove(m.unitPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove systemd unit: %w", err)
	}

	_ = exec.Command("systemctl", "daemon-reload").Run()
	return nil
}

func (m *linuxManager) Start() error {
	out, err := exec.Command("systemctl", "start", m.cfg.Name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl start: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (m *linuxManager) Stop() error {
	out, err := exec.Command("systemctl", "stop", m.cfg.Name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl stop: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (m *linuxManager) Status() (string, error) {
	out, _ := exec.Command("systemctl", "is-active", m.cfg.Name).CombinedOutput()
	status := strings.TrimSpace(string(out))
	switch status {
	case "active":
		return "RUNNING", nil
	case "inactive", "failed":
		return "STOPPED", nil
	case "activating":
		return "START_PENDING", nil
	case "deactivating":
		return "STOP_PENDING", nil
	default:
		return "UNKNOWN (" + status + ")", nil
	}
}

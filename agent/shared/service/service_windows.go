//go:build windows

package service

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

type windowsManager struct {
	cfg Config
}

// NewManager returns the Windows Service Control Manager implementation.
func NewManager(cfg Config) (Manager, error) {
	if cfg.Name == "" {
		cfg.Name = "endpoint-agent"
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = "Enterprise Endpoint Management Agent"
	}
	return &windowsManager{cfg: cfg}, nil
}

func (m *windowsManager) Install() error {
	exePath, err := GetExecutablePath()
	if err != nil {
		return err
	}

	// Build full binPath command line: "C:\path\to\agent.exe" arg1 arg2
	binPath := windowsBinPath(m.cfg, exePath)

	// Create service via sc.exe (Standard Windows Service Control Manager)
	// Explicitly assign obj= LocalSystem to guarantee maximum administrator/system privileges.
	createArgs := []string{
		"create", m.cfg.Name,
		"binPath=", binPath,
		"start=", "auto",
		"obj=", "LocalSystem",
		"DisplayName=", m.cfg.DisplayName,
	}
	if out, err := exec.Command("sc.exe", createArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("sc create failed: %s (%w)", strings.TrimSpace(string(out)), err)
	}

	// Set description
	if m.cfg.Description != "" {
		_ = exec.Command("sc.exe", "description", m.cfg.Name, m.cfg.Description).Run()
	}

	// Configure recovery action: restart service on failure (after 5s, 10s, 20s)
	failureArgs := []string{
		"failure", m.cfg.Name,
		"reset=", "86400",
		"actions=", "restart/5000/restart/10000/restart/20000",
	}
	_ = exec.Command("sc.exe", failureArgs...).Run()

	return nil
}

func (m *windowsManager) Uninstall() error {
	// Best effort stop first
	_ = m.Stop()

	out, err := exec.Command("sc.exe", "delete", m.cfg.Name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sc delete failed: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (m *windowsManager) Start() error {
	out, err := exec.Command("sc.exe", "start", m.cfg.Name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sc start failed: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (m *windowsManager) Stop() error {
	out, err := exec.Command("sc.exe", "stop", m.cfg.Name).CombinedOutput()
	if err != nil {
		// If already stopped, do not treat as fatal error
		if bytes.Contains(out, []byte("1062")) {
			return nil
		}
		return fmt.Errorf("sc stop failed: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (m *windowsManager) Status() (string, error) {
	out, err := exec.Command("sc.exe", "query", m.cfg.Name).CombinedOutput()
	if err != nil {
		return "NOT_INSTALLED", fmt.Errorf("sc query failed: %s (%w)", strings.TrimSpace(string(out)), err)
	}
	text := string(out)
	if strings.Contains(text, "RUNNING") {
		return "RUNNING", nil
	}
	if strings.Contains(text, "STOPPED") {
		return "STOPPED", nil
	}
	if strings.Contains(text, "START_PENDING") {
		return "START_PENDING", nil
	}
	if strings.Contains(text, "STOP_PENDING") {
		return "STOP_PENDING", nil
	}
	return "UNKNOWN", nil
}

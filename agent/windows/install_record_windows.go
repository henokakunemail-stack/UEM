//go:build windows

package windows

import (
	"bytes"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

//go:embed assets/endpoint-agent.ico
var agentIcon []byte

// UninstallKey is the Add/Remove Programs entry EndpointAgent-Setup.exe writes.
const UninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\EndpointAgent`

// SyncInstallRecord keeps the Control Panel entry in step with the binary that
// is actually running.
//
// The installer writes DisplayVersion once, at install time, and a self-update
// only swaps the executable, so a machine installed with 1.5.0 and updated to
// 1.5.6 kept showing 1.5.0 in Programs and Features while the console showed
// 1.5.6. The entry also pointed its icon at endpoint-agent.exe, which carries
// no icon resource, so the list showed a blank page. On every start the agent
// now writes its own version and its icon file into the entry.
//
// Only an existing entry for this install directory is touched: a machine
// installed by a script that registers no entry gets none. The installer is a 32-bit program, so its
// entry lives in the 32-bit registry view; both views are checked.
func SyncInstallRecord(version, installDir string) error {
	var errs []error
	for _, view := range []uint32{registry.WOW64_32KEY, registry.WOW64_64KEY} {
		if err := syncInstallRecord(registry.LOCAL_MACHINE, UninstallKey, view, version, installDir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func syncInstallRecord(root registry.Key, keyPath string, view uint32, version, installDir string) error {
	k, err := registry.OpenKey(root, keyPath, registry.QUERY_VALUE|registry.SET_VALUE|view)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()

	// The entry belongs to one install directory, named by its uninstaller. An
	// agent run from anywhere else (a copy under test, a second install) is not
	// the program the entry describes and must leave it alone.
	uninst, _, err := k.GetStringValue("UninstallString")
	if err != nil || !strings.EqualFold(filepath.Clean(filepath.Dir(strings.Trim(uninst, `" `))), filepath.Clean(installDir)) {
		return nil
	}

	if version != "" {
		if cur, _, err := k.GetStringValue("DisplayVersion"); err != nil || cur != version {
			if err := k.SetStringValue("DisplayVersion", version); err != nil {
				return err
			}
		}
	}

	iconPath := filepath.Join(installDir, "endpoint-agent.ico")
	if cur, err := os.ReadFile(iconPath); err != nil || !bytes.Equal(cur, agentIcon) {
		if err := os.WriteFile(iconPath, agentIcon, 0o644); err != nil {
			return err
		}
	}
	if cur, _, err := k.GetStringValue("DisplayIcon"); err != nil || cur != iconPath {
		return k.SetStringValue("DisplayIcon", iconPath)
	}
	return nil
}

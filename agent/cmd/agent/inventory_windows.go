//go:build windows

package main

import (
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
	winagent "github.com/henokakunemail-stack/Endpoint-Manager/agent/windows"
)

// newInventoryCollector returns the Windows inventory collector.
func newInventoryCollector() inventory.Collector { return winagent.NewCollector() }

// inventoryCapabilities are the command types this agent build can serve.
// The server stores these so it never sends a command an older agent would
// silently ignore.
func inventoryCapabilities() []string {
	return []string{"ping", "inventory.collect", "software.install", "exec.run", "term.open", "patch.scan", "patch.install", "software.uninstall", "software.uninstall.by_name"}
}

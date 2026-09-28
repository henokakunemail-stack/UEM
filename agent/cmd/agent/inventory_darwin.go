//go:build darwin

package main

import (
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
	macosagent "github.com/henokakunemail-stack/Endpoint-Manager/agent/macos"
)

// newInventoryCollector returns the macOS inventory collector.
func newInventoryCollector() inventory.Collector { return macosagent.NewCollector() }

// inventoryCapabilities are the command types this agent build can serve.
func inventoryCapabilities() []string {
	return []string{"ping", "inventory.collect", "software.install", "exec.run", "term.open", "patch.scan", "patch.install", "software.uninstall"}
}

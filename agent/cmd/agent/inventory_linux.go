//go:build linux

package main

import (
	linuxagent "github.com/henokakunemail-stack/Endpoint-Manager/agent/linux"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
)

// newInventoryCollector returns the Linux inventory collector.
func newInventoryCollector() inventory.Collector { return linuxagent.NewCollector() }

// inventoryCapabilities are the command types this agent build can serve.
func inventoryCapabilities() []string {
	return []string{"ping", "inventory.collect", "software.install", "exec.run", "term.open", "patch.scan", "patch.install", "software.uninstall", "software.uninstall.by_name"}
}

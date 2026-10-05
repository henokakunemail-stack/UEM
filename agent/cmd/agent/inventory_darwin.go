//go:build darwin

package main

import (
	macosagent "github.com/henokakunemail-stack/Endpoint-Manager/agent/macos"
	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
)

// newInventoryCollector returns the macOS inventory collector.
func newInventoryCollector() inventory.Collector { return macosagent.NewCollector() }

// inventoryCapabilities are the command types this agent build can serve.
//
// software.uninstall.by_name is deliberately NOT here. This build can look a
// program up only through pkgutil's receipt database (uninstall_darwin.go), while
// the inventory it reports is scanned from /Applications and keyed on the
// bundle's CFBundleName -- Slack's receipt is com.tinyspeck.slackmacgap, so the
// name an operator clicks does not match anything the lookup can find. Every
// request would come back "not installed", which this feature reports as success,
// so an operator would be shown a compliant device while the app is still there.
// Advertising the capability would mean selling a feature that cannot work; drop
// it here once the lookup reads the same source the inventory does.
func inventoryCapabilities() []string {
	return []string{"ping", "inventory.collect", "software.install", "exec.run", "term.open", "patch.scan", "patch.install", "software.uninstall"}
}

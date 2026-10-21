//go:build linux

package patch

import "os"

// Seams for tests.
var (
	rebootRequiredPath = "/var/run/reboot-required"
	debianMarkerPath   = "/etc/debian_version"
)

// RebootPending reports whether the OS says a reboot is needed to finish
// installing updates. Debian and Ubuntu write /var/run/reboot-required for
// that; on them its absence means no reboot is pending. Other distributions
// have no such marker, so the answer there is unknown (nil) and the server
// leaves its reboot flag as it was.
func RebootPending() *bool {
	yes, no := true, false
	if _, err := os.Stat(rebootRequiredPath); err == nil {
		return &yes
	}
	if _, err := os.Stat(debianMarkerPath); err == nil {
		return &no
	}
	return nil
}

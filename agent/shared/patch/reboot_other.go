//go:build !windows && !linux

package patch

// RebootPending has no standard source on this platform, so the answer is
// unknown and the server leaves its reboot flag as it was.
func RebootPending() *bool { return nil }

//go:build !windows

package remotecontrol

// No platform but Windows can capture yet, so there is no session to announce.
// When one can, it needs its own banner here rather than a silent session.
func showSessionBanner(text string) sessionBanner { return nil }

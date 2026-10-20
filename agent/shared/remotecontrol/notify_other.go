//go:build !windows && !linux

package remotecontrol

// Only Windows and Linux (X11) can capture so far, so there is no session to
// announce here. A platform that gains capture needs its own banner rather
// than a silent session.
func showSessionBanner(text string) sessionBanner { return nil }

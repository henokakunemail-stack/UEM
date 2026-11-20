//go:build !windows

package update

func isSharingViolation(error) bool { return false }

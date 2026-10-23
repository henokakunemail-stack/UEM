//go:build !windows

package main

// syncInstallRecord is a Windows concern: only there does an installer
// register an entry that a self-update leaves stale.
func syncInstallRecord() {}

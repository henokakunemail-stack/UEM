//go:build windows

package patch

import "golang.org/x/sys/windows/registry"

// RebootPending reports whether Windows has updates waiting on a reboot. These
// are the markers Windows Update and the servicing stack leave behind; when
// none is present no reboot is pending.
func RebootPending() *bool {
	yes, no := true, false
	for _, key := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`,
	} {
		if k, err := registry.OpenKey(registry.LOCAL_MACHINE, key, registry.QUERY_VALUE); err == nil {
			_ = k.Close()
			return &yes
		}
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager`, registry.QUERY_VALUE); err == nil {
		defer func() { _ = k.Close() }()
		if v, _, err := k.GetStringsValue("PendingFileRenameOperations"); err == nil && len(v) > 0 {
			return &yes
		}
	}
	return &no
}

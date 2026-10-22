//go:build windows

package windows

import (
	"unsafe"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// collectDomain reports the machine's Active Directory and Entra ID
// membership. Everything here is answered locally: NetGetJoinInformation and
// GetComputerNameEx read the machine's own join state, and the Entra ID join is
// recorded in the registry, so a collection never waits on a domain controller.
func collectDomain() *inventory.DomainMembership {
	var name *uint16
	var status uint32
	if err := windows.NetGetJoinInformation(nil, &name, &status); err != nil {
		return nil
	}
	netbios := windows.UTF16PtrToString(name)
	_ = windows.NetApiBufferFree((*byte)(unsafe.Pointer(name)))

	entra, tenant := entraJoin()
	return inventory.WindowsDomain(status, netbios, computerNameEx(windows.ComputerNameDnsDomain), entra, tenant)
}

// computerNameEx returns one of the GetComputerNameEx names, or "" when the
// machine has none of that kind (a workgroup machine has no DNS domain).
func computerNameEx(kind uint32) string {
	var n uint32
	_ = windows.GetComputerNameEx(kind, nil, &n) // sizes the buffer
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n)
	if err := windows.GetComputerNameEx(kind, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// entraJoin reports a Microsoft Entra ID (Azure AD) join and the tenant's
// display name. A joined device has a certificate thumbprint subkey under
// CloudDomainJoin\JoinInfo naming its tenant; a device that is only
// "registered" (a work account added to a personal PC) has none, and is not
// joined.
func entraJoin() (bool, string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\CloudDomainJoin\JoinInfo`, registry.READ)
	if err != nil {
		return false, ""
	}
	defer k.Close()
	subs, err := k.ReadSubKeyNames(-1)
	if err != nil || len(subs) == 0 {
		return false, ""
	}
	tenant := ""
	if jk, err := registry.OpenKey(k, subs[0], registry.READ); err == nil {
		if id, _, err := jk.GetStringValue("TenantId"); err == nil && id != "" {
			if tk, err := registry.OpenKey(registry.LOCAL_MACHINE,
				`SYSTEM\CurrentControlSet\Control\CloudDomainJoin\TenantInfo\`+id, registry.READ); err == nil {
				tenant, _, _ = tk.GetStringValue("DisplayName")
				tk.Close()
			}
			if tenant == "" {
				tenant = id
			}
		}
		jk.Close()
	}
	return true, tenant
}

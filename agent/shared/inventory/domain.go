package inventory

import (
	"bufio"
	"strings"
)

// Directory types a machine can be joined to.
const (
	DomainActiveDirectory = "active_directory" // on-premises AD (Windows, sssd/winbind, macOS dsconfigad)
	DomainEntraID         = "entra_id"         // Microsoft Entra ID (Azure AD) joined
	DomainHybrid          = "hybrid"           // both AD and Entra ID
	DomainFreeIPA         = "freeipa"          // sssd id_provider = ipa
	DomainOther           = "other"            // a directory sssd knows but not one of the above
	DomainWorkgroup       = "workgroup"        // Windows, not joined: Name is the workgroup
	DomainNone            = "none"             // not joined to any directory
)

// DomainMembership says whether the machine is joined to a directory domain.
type DomainMembership struct {
	Joined bool `json:"joined"`
	// Type is one of the Domain* constants.
	Type string `json:"type"`
	// Name is the domain (DNS name when the OS gives one), or the workgroup
	// for a Windows machine that is not joined.
	Name string `json:"name,omitempty"`
	// Tenant is the Entra ID tenant's display name, when Type involves it.
	Tenant string `json:"tenant,omitempty"`
}

// Windows NetGetJoinInformation status values (NETSETUP_JOIN_STATUS).
const (
	WinJoinUnknown   = 0
	WinJoinUnjoined  = 1
	WinJoinWorkgroup = 2
	WinJoinDomain    = 3
)

// WindowsDomain combines what Windows reports: the NetGetJoinInformation
// status and name (a NetBIOS domain or a workgroup), the DNS domain from
// GetComputerNameEx, and whether the device is Entra ID joined. A domain
// machine is named by its DNS domain when it has one, since that is the name
// an administrator recognises; the NetBIOS name is the fallback.
func WindowsDomain(status uint32, netbiosOrWorkgroup, dnsDomain string, entraJoined bool, tenant string) *DomainMembership {
	ad := status == WinJoinDomain
	name := strings.TrimSpace(netbiosOrWorkgroup)
	if ad && strings.TrimSpace(dnsDomain) != "" {
		name = strings.TrimSpace(dnsDomain)
	}
	switch {
	case ad && entraJoined:
		return &DomainMembership{Joined: true, Type: DomainHybrid, Name: name, Tenant: tenant}
	case ad:
		return &DomainMembership{Joined: true, Type: DomainActiveDirectory, Name: name}
	case entraJoined:
		return &DomainMembership{Joined: true, Type: DomainEntraID, Name: tenant, Tenant: tenant}
	case status == WinJoinWorkgroup:
		return &DomainMembership{Joined: false, Type: DomainWorkgroup, Name: name}
	case status == WinJoinUnjoined:
		return &DomainMembership{Joined: false, Type: DomainNone}
	default:
		return nil
	}
}

// LinuxDomain reads the two ways a Linux host joins a directory: an sssd
// domain (realmd and ipa-client both write one) and winbind (security = ads in
// smb.conf). Either file may be empty or missing. sssd wins when both are
// configured, since that is the one realmd sets up.
func LinuxDomain(sssdConf, smbConf string) *DomainMembership {
	if m := parseSSSD(sssdConf); m != nil {
		return m
	}
	if m := parseSmbADS(smbConf); m != nil {
		return m
	}
	return &DomainMembership{Joined: false, Type: DomainNone}
}

// parseSSSD returns the first domain the [sssd] section enables. Its type
// comes from that domain's id_provider.
func parseSSSD(conf string) *DomainMembership {
	sections := iniSections(conf)
	var domain string
	for _, d := range strings.Split(sections["sssd"]["domains"], ",") {
		if d = strings.TrimSpace(d); d != "" {
			domain = d
			break
		}
	}
	if domain == "" {
		return nil
	}
	sec := sections["domain/"+strings.ToLower(domain)]
	m := &DomainMembership{Joined: true, Type: DomainOther, Name: domain}
	switch strings.ToLower(sec["id_provider"]) {
	case "ad":
		m.Type = DomainActiveDirectory
	case "ipa":
		m.Type = DomainFreeIPA
	}
	if r := sec["ad_domain"]; r != "" {
		m.Name = r
	} else if r := sec["ipa_domain"]; r != "" {
		m.Name = r
	}
	return m
}

// parseSmbADS reports a winbind AD member: security = ads in [global], named
// by its realm.
func parseSmbADS(conf string) *DomainMembership {
	g := iniSections(conf)["global"]
	if !strings.EqualFold(g["security"], "ads") {
		return nil
	}
	name := strings.ToLower(g["realm"])
	if name == "" {
		name = g["workgroup"]
	}
	return &DomainMembership{Joined: true, Type: DomainActiveDirectory, Name: name}
}

// MacDomain reads `dsconfigad -show`, which prints nothing when the Mac is not
// bound and "Active Directory Domain = corp.example.com" when it is.
func MacDomain(dsconfigadShow string) *DomainMembership {
	sc := bufio.NewScanner(strings.NewReader(dsconfigadShow))
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "Active Directory Domain") {
			if v := strings.TrimSpace(val); v != "" {
				return &DomainMembership{Joined: true, Type: DomainActiveDirectory, Name: v}
			}
		}
	}
	return &DomainMembership{Joined: false, Type: DomainNone}
}

// iniSections parses the INI dialect sssd.conf and smb.conf share: [section]
// headers, key = value lines, # and ; comments. Section names and keys are
// lower-cased; values keep their case.
func iniSections(conf string) map[string]map[string]string {
	out := map[string]map[string]string{}
	cur := ""
	sc := bufio.NewScanner(strings.NewReader(conf))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' && strings.HasSuffix(line, "]") {
			cur = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if out[cur] == nil {
			out[cur] = map[string]string{}
		}
		out[cur][strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(val)
	}
	return out
}

package inventory

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWindowsDomain(t *testing.T) {
	cases := []struct {
		name                 string
		status               uint32
		netbios, dns, tenant string
		entra                bool
		want                 *DomainMembership
	}{
		{"AD member named by its DNS domain", WinJoinDomain, "CORP", "corp.example.com", "", false,
			&DomainMembership{Joined: true, Type: DomainActiveDirectory, Name: "corp.example.com"}},
		{"AD member with no DNS domain falls back to NetBIOS", WinJoinDomain, "CORP", "", "", false,
			&DomainMembership{Joined: true, Type: DomainActiveDirectory, Name: "CORP"}},
		{"hybrid join", WinJoinDomain, "CORP", "corp.example.com", "Contoso", true,
			&DomainMembership{Joined: true, Type: DomainHybrid, Name: "corp.example.com", Tenant: "Contoso"}},
		{"Entra ID only: the workgroup is not the domain", WinJoinWorkgroup, "WORKGROUP", "", "Contoso", true,
			&DomainMembership{Joined: true, Type: DomainEntraID, Name: "Contoso", Tenant: "Contoso"}},
		{"workgroup", WinJoinWorkgroup, "WORKGROUP", "", "", false,
			&DomainMembership{Joined: false, Type: DomainWorkgroup, Name: "WORKGROUP"}},
		{"unjoined", WinJoinUnjoined, "", "", "", false, &DomainMembership{Joined: false, Type: DomainNone}},
		{"unknown stays unknown", WinJoinUnknown, "", "", "", false, nil},
	}
	for _, c := range cases {
		got := WindowsDomain(c.status, c.netbios, c.dns, c.entra, c.tenant)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestLinuxDomain(t *testing.T) {
	realmd := `
[sssd]
domains = corp.example.com
config_file_version = 2
services = nss, pam

[domain/corp.example.com]
default_shell = /bin/bash
ad_domain = corp.example.com
krb5_realm = CORP.EXAMPLE.COM
id_provider = ad
`
	ipa := "[sssd]\ndomains = ipa.example.org\n\n[domain/ipa.example.org]\nid_provider = ipa\nipa_domain = ipa.example.org\n"
	winbind := "[global]\n   workgroup = CORP\n   security = ADS\n   realm = CORP.EXAMPLE.COM\n"
	standalone := "[global]\n   workgroup = WORKGROUP\n   security = user\n"

	cases := []struct {
		name, sssd, smb string
		want            *DomainMembership
	}{
		{"realmd AD join", realmd, "", &DomainMembership{Joined: true, Type: DomainActiveDirectory, Name: "corp.example.com"}},
		{"FreeIPA client", ipa, "", &DomainMembership{Joined: true, Type: DomainFreeIPA, Name: "ipa.example.org"}},
		{"winbind AD member", "", winbind, &DomainMembership{Joined: true, Type: DomainActiveDirectory, Name: "corp.example.com"}},
		{"a samba file server is not a domain member", "", standalone, &DomainMembership{Joined: false, Type: DomainNone}},
		{"sssd with no domain enabled", "[sssd]\nservices = nss\n", "", &DomainMembership{Joined: false, Type: DomainNone}},
		{"nothing configured", "", "", &DomainMembership{Joined: false, Type: DomainNone}},
	}
	for _, c := range cases {
		if got := LinuxDomain(c.sssd, c.smb); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestMacDomain(t *testing.T) {
	bound := "Active Directory Forest          = corp.example.com\nActive Directory Domain          = corp.example.com\nComputer Account                 = mac01$\n"
	if got := MacDomain(bound); !reflect.DeepEqual(got, &DomainMembership{Joined: true, Type: DomainActiveDirectory, Name: "corp.example.com"}) {
		t.Errorf("bound Mac: got %+v", got)
	}
	if got := MacDomain(""); !reflect.DeepEqual(got, &DomainMembership{Joined: false, Type: DomainNone}) {
		t.Errorf("unbound Mac: got %+v", got)
	}
}

// The field is additive on the wire: an agent that could not tell leaves it
// out entirely, which the console reads as unknown, and the server stores the
// os section as it arrives.
func TestDomainIsOmittedWhenUnknown(t *testing.T) {
	b, _ := json.Marshal(OSDetail{Edition: "Pro"})
	if string(b) != `{"edition":"Pro","build_number":"","architecture":""}` {
		t.Errorf("unknown domain serialised as %s", b)
	}
	b, _ = json.Marshal(OSDetail{Domain: &DomainMembership{Joined: false, Type: DomainWorkgroup, Name: "WORKGROUP"}})
	var back map[string]map[string]any
	_ = json.Unmarshal(b, &back)
	if back["domain"]["joined"] != false || back["domain"]["type"] != "workgroup" {
		t.Errorf("domain serialised as %s", b)
	}
}

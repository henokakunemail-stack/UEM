//go:build windows

package windows

import (
	"testing"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/inventory"
)

// Every Windows machine is either joined to a domain, in a workgroup, or
// Entra ID joined, and NetGetJoinInformation answers locally, so the report
// always carries a membership. Nil here would show as "unknown" in the console
// for a fact Windows always knows.
func TestTheInventoryReportsDomainMembership(t *testing.T) {
	d := collectDomain()
	if d == nil {
		t.Fatal("no domain membership collected")
	}
	switch d.Type {
	case inventory.DomainActiveDirectory, inventory.DomainHybrid, inventory.DomainEntraID:
		if !d.Joined || d.Name == "" {
			t.Errorf("joined machine reported as %+v", d)
		}
	case inventory.DomainWorkgroup, inventory.DomainNone:
		if d.Joined {
			t.Errorf("unjoined machine reported as joined: %+v", d)
		}
	default:
		t.Errorf("unexpected membership type %+v", d)
	}
	t.Logf("this machine: %+v", *d)
}

package usermgmt

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

// The console needs at least one active administrator: without one nobody
// can manage users again. Demoting or deactivating the last one is refused on
// every path that can do it.
func TestTheLastActiveAdminCannotBeRemoved(t *testing.T) {
	cases := []struct {
		name, method, payload string
	}{
		{"demote", "PUT", `{"role":"viewer"}`},
		{"deactivate via edit", "PUT", `{"is_active":false}`},
		{"deactivate", "DELETE", ""},
	}
	for _, c := range cases {
		t.Run(c.name+" refused when last", func(t *testing.T) {
			e := newEnv(t)
			boss := e.seed("boss", "boss-password-1", rbac.RoleAdmin)
			rec := e.call(c.method, "/api/users/"+boss, rbac.RoleAdmin, c.payload)
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"last_admin"`) {
				t.Fatalf("%s = %d %s, want 409 last_admin", c.name, rec.Code, rec.Body.String())
			}
			if e.roleOf(boss) != rbac.RoleAdmin || !e.activeOf(boss) {
				t.Fatal("the refused change was written anyway")
			}
		})
		t.Run(c.name+" allowed with another admin", func(t *testing.T) {
			e := newEnv(t)
			boss := e.seed("boss", "boss-password-1", rbac.RoleAdmin)
			e.seed("deputy", "deputy-password-1", rbac.RoleAdmin)
			if rec := e.call(c.method, "/api/users/"+boss, rbac.RoleAdmin, c.payload); rec.Code != http.StatusOK {
				t.Fatalf("%s = %d %s, want 200", c.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// deactivateUser refused deactivating yourself; the edit route did not.
func TestAnAdminCannotDeactivateThemselvesViaEdit(t *testing.T) {
	e := newEnv(t)
	e.seedWithID(actorID, "actor", "actor-password-1", rbac.RoleAdmin)
	e.seed("deputy", "deputy-password-1", rbac.RoleAdmin)
	rec := e.call("PUT", "/api/users/"+actorID, rbac.RoleAdmin, `{"is_active":false}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"cannot_deactivate_self"`) {
		t.Fatalf("self-deactivate via PUT = %d %s, want 400 cannot_deactivate_self", rec.Code, rec.Body.String())
	}
	if !e.activeOf(actorID) {
		t.Fatal("the account was deactivated anyway")
	}
	// Editing your own display name stays allowed.
	if rec := e.call("PUT", "/api/users/"+actorID, rbac.RoleAdmin, `{"display_name":"Me"}`); rec.Code != http.StatusOK {
		t.Fatalf("renaming yourself = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// Two admins deactivating each other at the same moment must not both
// succeed: the count and the write are one unit.
func TestConcurrentRemovalsLeaveAnActiveAdmin(t *testing.T) {
	for round := 0; round < 10; round++ {
		e := newEnv(t)
		a := e.seed("alpha", "alpha-password-1", rbac.RoleAdmin)
		b := e.seed("beta", "beta-password-1", rbac.RoleAdmin)
		var wg sync.WaitGroup
		for _, id := range []string{a, b} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				e.call("DELETE", "/api/users/"+id, rbac.RoleAdmin, "")
			}(id)
		}
		wg.Wait()
		if !e.activeOf(a) && !e.activeOf(b) {
			t.Fatalf("round %d: both admins were deactivated", round)
		}
	}
}

// The audit entry says what changed, so a role change from X to Y can be read
// back from the trail.
func TestUpdateAuditRecordsWhatChanged(t *testing.T) {
	e := newEnv(t)
	victim := e.seed("victim", "victim-password-1", rbac.RoleTechnician)
	if rec := e.call("PUT", "/api/users/"+victim, rbac.RoleAdmin, `{"role":"viewer"}`); rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}
	d := e.audit.details[len(e.audit.details)-1]
	if d["role_from"] != rbac.RoleTechnician || d["role_to"] != rbac.RoleViewer {
		t.Fatalf("audit details %v, want role_from technician and role_to viewer", d)
	}
	if _, ok := d["display_name_from"]; ok {
		t.Fatalf("an unchanged display name was recorded as a change: %v", d)
	}
}

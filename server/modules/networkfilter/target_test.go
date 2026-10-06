package networkfilter

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// targetFixture uses the real migrated schema rather than nfFixture's hand-built
// tables: these tests exercise createPolicy, which writes to filter_policies, so
// the table has to exist.
func targetFixture(t *testing.T) (*Handler, *sqlx.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	now := time.Now().UTC()
	database.MustExec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-a','PC-A','windows','11.0','1.0.0','Jakarta','online',?,?,'h',?,?)`,
		now, now, now, now)
	database.MustExec(`INSERT INTO device_groups (id, name, description, created_at, updated_at)
		VALUES ('grp-fin','Finance','',?,?)`, now, now)

	// Validation is the only thing under test, so the middleware is a pass-through
	// and the RBAC decorator is not in the way.
	h := NewHandler(NewRepository(database), nil,
		devicemgmt.NewRepository(database), nopAudit{},
		func(n http.Handler) http.Handler { return n })
	return h, database
}

func postPolicy(t *testing.T, h *Handler, body createPolicyReq) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/filter/policies", bytes.NewReader(raw))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.createPolicy(rec, req)
	return rec
}

// A mistyped device id was accepted, saved, listed in the policy table, and blocked
// nothing. The operator's only clue was that the device's rule count never moved --
// which reads like the device ignoring the policy rather than like a policy pointing
// at nothing.
func TestPolicyTargetingAnUnknownDeviceIsRejected(t *testing.T) {
	h, _ := targetFixture(t)

	rec := postPolicy(t, h, createPolicyReq{Name: "typo", TargetType: "device", TargetID: "dev-aa"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a policy aimed at a device that does not exist was "+
			"stored and will silently match nothing (body: %s)", rec.Code, rec.Body.String())
	}
}

// Same for a group. The console now offers a picker for both, so this only fires
// on a direct API call or a stale client -- but it costs nothing to refuse.
func TestPolicyTargetingAnUnknownGroupIsRejected(t *testing.T) {
	h, _ := targetFixture(t)

	rec := postPolicy(t, h, createPolicyReq{Name: "typo", TargetType: "group", TargetID: "grp-financ"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// target_type='device' with no target at all is the state this bug actually
// produced, and it is the one the operator could create from the old form by
// leaving the free-text field blank.
func TestPolicyWithADeviceScopeAndNoTargetIsRejected(t *testing.T) {
	h, _ := targetFixture(t)

	rec := postPolicy(t, h, createPolicyReq{Name: "empty", TargetType: "device"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a device-scoped policy with no target matches "+
			"no device at all (body: %s)", rec.Code, rec.Body.String())
	}
}

// An unrecognised scope used to be stored verbatim and then matched nothing.
func TestPolicyWithAnUnknownScopeIsRejected(t *testing.T) {
	h, _ := targetFixture(t)

	rec := postPolicy(t, h, createPolicyReq{Name: "odd", TargetType: "laptop"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// Fleet-wide is the whole fleet, so an empty target is its correct value rather
// than a missing one -- refusing it would break the common case.
func TestFleetWidePolicyNeedsNoTarget(t *testing.T) {
	h, _ := targetFixture(t)

	if rec := postPolicy(t, h, createPolicyReq{Name: "everyone"}); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	if rec := postPolicy(t, h, createPolicyReq{Name: "everyone explicit", TargetType: "all", TargetID: "ignored"}); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: an explicit 'all' must still be accepted (body: %s)",
			rec.Code, rec.Body.String())
	}
}

func TestPolicyTargetingAKnownDeviceOrGroupIsAccepted(t *testing.T) {
	h, _ := targetFixture(t)

	for _, tc := range []createPolicyReq{
		{Name: "one laptop", TargetType: "device", TargetID: "dev-a"},
		{Name: "finance", TargetType: "group", TargetID: "grp-fin"},
	} {
		if rec := postPolicy(t, h, tc); rec.Code != http.StatusCreated {
			t.Errorf("%q: status = %d, want 201 (body: %s)", tc.Name, rec.Code, rec.Body.String())
		}
	}
}

// An allow rule was accepted, stored, shown in the rules table, and then dropped at
// enforcement time by the compile query's action='block' filter. The console showed a
// rule that enforced nothing.
func TestAllowRuleIsRejectedWithAnExplanation(t *testing.T) {
	h, _ := targetFixture(t)

	created := postPolicy(t, h, createPolicyReq{Name: "p", TargetType: "all"})
	if created.Code != http.StatusCreated {
		t.Fatalf("setup: status = %d (body: %s)", created.Code, created.Body.String())
	}
	var policy FilterPolicy
	if err := json.Unmarshal(created.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}

	rec := postRule(t, h, policy.ID, addRuleReq{Pattern: "detik.com", Action: "allow"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: an allow rule is stored and then discarded at "+
			"enforcement time (body: %s)", rec.Code, rec.Body.String())
	}
}

// 'ip_port' reached the hosts file, where the engine keeps only the text before the
// first "/" -- so 198.51.100.0/24:443 was written as a bare hostname and blocked
// nothing that anyone was browsing.
func TestIPPortRuleIsRejectedWithAnExplanation(t *testing.T) {
	h, _ := targetFixture(t)

	created := postPolicy(t, h, createPolicyReq{Name: "p", TargetType: "all"})
	if created.Code != http.StatusCreated {
		t.Fatalf("setup: status = %d (body: %s)", created.Code, created.Body.String())
	}
	var policy FilterPolicy
	if err := json.Unmarshal(created.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}

	rec := postRule(t, h, policy.ID, addRuleReq{Pattern: "198.51.100.0/24:443", RuleType: "ip_port"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// The rule form still posts action and rule_type from older clients, so the
// explicit values must keep working.
func TestAnOrdinaryDomainBlockIsAccepted(t *testing.T) {
	h, _ := targetFixture(t)

	created := postPolicy(t, h, createPolicyReq{Name: "p", TargetType: "all"})
	if created.Code != http.StatusCreated {
		t.Fatalf("setup: status = %d (body: %s)", created.Code, created.Body.String())
	}
	var policy FilterPolicy
	if err := json.Unmarshal(created.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}

	rec := postRule(t, h, policy.ID, addRuleReq{Pattern: "detik.com", Action: "block", RuleType: "domain"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
}

func postRule(t *testing.T, h *Handler, policyID string, body addRuleReq) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/filter/policies/x/rules", bytes.NewReader(raw))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", policyID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.addRule(rec, req)
	return rec
}

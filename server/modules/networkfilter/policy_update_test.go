package networkfilter

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
)

// scopedPolicyFixture is targetFixture plus one saved policy aimed at a single
// device, which is the row every test here edits.
func scopedPolicyFixture(t *testing.T) (*Handler, *sqlx.DB, string) {
	t.Helper()
	h, database := targetFixture(t)

	now := time.Now().UTC()
	database.MustExec(`INSERT INTO filter_policies
		(id, name, description, target_type, target_id, is_enabled, priority, created_by, created_at, updated_at)
		VALUES ('pol-1','Block social media','applies to the design floor','device','dev-a',1,100,'u-1',?,?)`,
		now, now)
	return h, database, "pol-1"
}

func putPolicy(t *testing.T, h *Handler, id string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/filter/policies/"+id, bytes.NewReader(raw))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.updatePolicy(rec, req)
	return rec
}

func policyRow(t *testing.T, database *sqlx.DB, id string) (name, targetType, targetID, description string, enabled int) {
	t.Helper()
	err := database.QueryRowx(`SELECT name, target_type, target_id, description, is_enabled
		FROM filter_policies WHERE id = ?`, id).Scan(&name, &targetType, &targetID, &description, &enabled)
	if err != nil {
		t.Fatalf("read policy %s: %v", id, err)
	}
	return name, targetType, targetID, description, enabled
}

// A partial update must change only the fields it names.
//
// updatePolicy decoded into createPolicyReq, whose fields are plain strings, then
// assigned two of them unconditionally: policy.Description and policy.TargetID.
// JSON leaves an absent field and an explicitly empty one both as "", so a client
// sending {"is_enabled": false} -- exactly what the console's enable/disable toggle
// sends -- blanked target_id on the way in. The handler then validated that empty
// scope and answered 400, so a policy scoped to one device or group could not be
// disabled at all: the toggle did nothing and the operator had no error on screen
// beyond the request failing. Disabling a blacklist for one machine is the whole
// reason to scope a policy in the first place.
func TestAPartialPolicyUpdateKeepsTheScopeItDoesNotMention(t *testing.T) {
	h, database, id := scopedPolicyFixture(t)

	rec := putPolicy(t, h, id, map[string]any{"is_enabled": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: disabling a scoped policy must not be rejected "+
			"(body: %s)", rec.Code, rec.Body.String())
	}

	name, targetType, targetID, description, enabled := policyRow(t, database, id)
	if targetID != "dev-a" {
		t.Errorf("target_id = %q, want \"dev-a\": an update that never mentioned the scope cleared it", targetID)
	}
	if targetType != "device" {
		t.Errorf("target_type = %q, want \"device\"", targetType)
	}
	if name != "Block social media" {
		t.Errorf("name = %q; an update that never mentioned the name changed it", name)
	}
	if description != "applies to the design floor" {
		t.Errorf("description = %q; an update that never mentioned the description cleared it", description)
	}
	if enabled != 0 {
		t.Errorf("is_enabled = %d, want 0: the one field the update did name was not applied", enabled)
	}
}

// The pointer types have to distinguish absent from empty in the other direction
// too. A policy whose scope should go back to the whole fleet legitimately sends
// target_id: "", and if that read as "not mentioned" a policy could never be
// rescoped -- the reason the fields became pointers in the first place.
func TestAScopedPolicyCanBeWidenedBackToTheWholeFleet(t *testing.T) {
	h, database, id := scopedPolicyFixture(t)

	rec := putPolicy(t, h, id, map[string]any{"target_type": "all", "target_id": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: clearing the scope must be accepted (body: %s)",
			rec.Code, rec.Body.String())
	}

	_, targetType, targetID, _, _ := policyRow(t, database, id)
	if targetType != "all" || targetID != "" {
		t.Errorf("scope = (%q, %q), want (\"all\", \"\"): target_id \"\" is a request to clear the "+
			"scope, not to ignore it", targetType, targetID)
	}
}

// An empty body is a request to change nothing, and it must not be a request to
// clear everything. The console sends partial bodies by design, so this shape is
// one a future caller will produce.
func TestAnEmptyPolicyUpdateChangesNothing(t *testing.T) {
	h, database, id := scopedPolicyFixture(t)

	rec := putPolicy(t, h, id, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	name, targetType, targetID, description, enabled := policyRow(t, database, id)
	if targetType != "device" || targetID != "dev-a" || name != "Block social media" ||
		description != "applies to the design floor" || enabled != 1 {
		t.Errorf("row is now (%q, %q, %q, %q, %d), want the original: an empty body stored something",
			targetType, targetID, name, description, enabled)
	}
}

// A value the row cannot hold is refused rather than stored. An unnamed policy and
// an unrecognised scope are both states no operator asked for and the console
// cannot render, and a device scope with no target matches no device at all -- the
// same silent no-op createPolicy was fixed for in target_test.go.
func TestAPolicyUpdateRefusesAValueItCannotStore(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"blank name", map[string]any{"name": ""}},
		{"blank scope", map[string]any{"target_type": ""}},
		{"unknown scope", map[string]any{"target_type": "everything"}},
		{"zero priority", map[string]any{"priority": 0}},
		{"negative priority", map[string]any{"priority": -5}},
		{"device scope with no target", map[string]any{"target_type": "device", "target_id": ""}},
		{"unknown device", map[string]any{"target_type": "device", "target_id": "dev-aa"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, database, id := scopedPolicyFixture(t)

			rec := putPolicy(t, h, id, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s would be stored in a row that cannot hold it "+
					"(body: %s)", rec.Code, tc.name, rec.Body.String())
			}

			name, targetType, targetID, _, _ := policyRow(t, database, id)
			if targetType != "device" || targetID != "dev-a" || name != "Block social media" {
				t.Errorf("a refused update still wrote to the row: (name %q, %q, %q)",
					name, targetType, targetID)
			}
		})
	}
}

package networkfilter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func (f *connectFixture) policyID(t *testing.T) string {
	t.Helper()
	policies, err := f.repo.ListPolicies(context.Background())
	if err != nil || len(policies) != 1 {
		t.Fatalf("fixture policies: %v %v", policies, err)
	}
	return policies[0].ID
}

func callRuleRoute(h http.HandlerFunc, path, policyID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", policyID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func (f *connectFixture) storedPatterns(t *testing.T, policyID string) []string {
	t.Helper()
	rules, err := f.repo.ListRulesByPolicy(context.Background(), policyID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Pattern)
	}
	return out
}

// TestAddRuleRejectsInvalidAndDuplicate: patterns were stored verbatim, so
// "intranet" or "10.0.0.1" became rules that enforced nothing, and
// "https://Detik.com/" sat next to "detik.com" as a second copy.
func TestAddRuleRejectsInvalidAndDuplicate(t *testing.T) {
	f := newConnectFixture(t)
	id := f.policyID(t)

	rec := callRuleRoute(f.h.addRule, "/api/filter/policies/"+id+"/rules", id, `{"pattern":"intranet"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"no_tld"`) {
		t.Errorf("single-label pattern -> %d %s, want 400 no_tld", rec.Code, rec.Body.String())
	}
	rec = callRuleRoute(f.h.addRule, "/api/filter/policies/"+id+"/rules", id, `{"pattern":"https://Detik.com/news"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"duplicate_existing"`) {
		t.Errorf("duplicate of detik.com -> %d %s, want 409 duplicate_existing", rec.Code, rec.Body.String())
	}
	rec = callRuleRoute(f.h.addRule, "/api/filter/policies/"+id+"/rules", id, `{"pattern":"HTTPS://Kompas.com/x"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid pattern -> %d %s", rec.Code, rec.Body.String())
	}
	if got := f.storedPatterns(t, id); strings.Join(got, ",") != "detik.com,kompas.com" {
		t.Errorf("stored patterns = %v, want [detik.com kompas.com] in normalized form", got)
	}
}

// TestBulkAddReportsPerLineAndDispatchesOnce: a pasted list is checked line by
// line, and the affected device gets exactly one re-dispatch for the whole list
// rather than one per domain.
func TestBulkAddReportsPerLineAndDispatchesOnce(t *testing.T) {
	f := newConnectFixture(t)
	id := f.policyID(t)

	body := `{"patterns":["tiktok.com","https://www.Reddit.com/r/x","detik.com","tiktok.com","not a domain","10.1.1.1","*.example.org"]}`
	rec := callRuleRoute(f.h.addRulesBulk, "/api/filter/policies/"+id+"/rules/bulk", id, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk -> %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Added, Duplicates, Invalid int
		Results                    []BulkRuleResult
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Added != 3 || resp.Duplicates != 2 || resp.Invalid != 2 || len(resp.Results) != 7 {
		t.Fatalf("summary = %+v, want 3 added, 2 duplicates, 2 invalid over 7 lines", resp)
	}
	want := []struct{ status, reason string }{
		{"added", ""}, {"added", ""}, {"duplicate", ReasonDuplicate}, {"duplicate", ReasonDuplicateBatch},
		{"invalid", ReasonInvalidChars}, {"invalid", ReasonIPLiteral}, {"added", ""},
	}
	for i, w := range want {
		if got := resp.Results[i]; got.Status != w.status || got.Reason != w.reason {
			t.Errorf("line %d (%q) = %s/%s, want %s/%s", i, got.Input, got.Status, got.Reason, w.status, w.reason)
		}
	}
	if len(f.hub.sent) != 1 {
		t.Errorf("%d dispatches for one bulk import, want 1", len(f.hub.sent))
	}
	if got := f.storedPatterns(t, id); strings.Join(got, ",") != "detik.com,tiktok.com,www.reddit.com,example.org" {
		t.Errorf("stored = %v", got)
	}
}

// TestBulkAddIsAtomic: a batch that fails partway stores nothing.
func TestBulkAddIsAtomic(t *testing.T) {
	f := newConnectFixture(t)
	id := f.policyID(t)
	existing, err := f.repo.ListRulesByPolicy(context.Background(), id)
	if err != nil || len(existing) != 1 {
		t.Fatal(existing, err)
	}
	// The second rule reuses an existing primary key, so its insert fails after
	// the first one has already gone in.
	err = f.repo.AddRules(context.Background(), []*FilterRule{
		{PolicyID: id, Pattern: "first.example", Action: "block"},
		{ID: existing[0].ID, PolicyID: id, Pattern: "second.example", Action: "block"},
	})
	if err == nil {
		t.Fatal("AddRules accepted a duplicate primary key")
	}
	if got := f.storedPatterns(t, id); strings.Join(got, ",") != "detik.com" {
		t.Errorf("after a failed batch the policy holds %v, want only [detik.com]", got)
	}
}

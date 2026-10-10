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
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

func newDispatchFixture(t *testing.T) (*Handler, *Repository, *fakeHub) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "autodispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	repo := NewRepository(database)
	now := time.Now().UTC()

	if _, err := database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-online', 'PC-ONLINE', 'windows', '11.0', '1.0.0', 'Jakarta', 'online', ?, ?, 'hash', ?, ?)
	`, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	hub := &fakeHub{online: true}
	deviceRepo := devicemgmt.NewRepository(database)
	h := NewHandler(repo, hub, deviceRepo, nopAudit{}, nil)
	return h, repo, hub
}

func withUserCtx(r *http.Request, userID string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), auth.CtxUserID, userID))
}

func TestAutoDispatchOnRuleAddAndRuleDelete(t *testing.T) {
	h, repo, hub := newDispatchFixture(t)
	ctx := context.Background()

	// 1. Create a policy targeted at dev-online
	pol := &FilterPolicy{
		Name:       "Test Block",
		TargetType: "device",
		TargetID:   "dev-online",
		IsEnabled:  true,
		Priority:   100,
		CreatedBy:  "admin",
	}
	if err := repo.CreatePolicy(ctx, pol); err != nil {
		t.Fatal(err)
	}
	hub.sent = nil // Reset any messages sent during policy creation

	// 2. Add rule via handler (POST /api/filter/policies/{id}/rules)
	ruleBody, _ := json.Marshal(map[string]string{
		"pattern":  "detik.com",
		"action":   "block",
		"category": "custom",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/filter/policies/"+pol.ID+"/rules", bytes.NewReader(ruleBody))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", pol.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = withUserCtx(req, "admin")
	rec := httptest.NewRecorder()

	h.addRule(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("addRule code = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}

	// Verify WebSocket envelope was automatically dispatched
	if len(hub.sent) != 1 {
		t.Fatalf("hub received %d messages after addRule, want 1", len(hub.sent))
	}
	var env struct {
		Command string `json:"command"`
		Payload struct {
			BlockedDomains []string `json:"blocked_domains"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(hub.sent[0], &env); err != nil {
		t.Fatal(err)
	}
	if env.Command != "filter.apply" {
		t.Errorf("command = %q, want filter.apply", env.Command)
	}
	if len(env.Payload.BlockedDomains) != 1 || env.Payload.BlockedDomains[0] != "detik.com" {
		t.Errorf("blocked_domains = %v, want [detik.com]", env.Payload.BlockedDomains)
	}

	var addedRule FilterRule
	_ = json.Unmarshal(rec.Body.Bytes(), &addedRule)

	// 3. Delete rule via handler (DELETE /api/filter/rules/{ruleId})
	hub.sent = nil
	delReq := httptest.NewRequest(http.MethodDelete, "/api/filter/rules/"+addedRule.ID, nil)
	delRctx := chi.NewRouteContext()
	delRctx.URLParams.Add("ruleId", addedRule.ID)
	delReq = delReq.WithContext(context.WithValue(delReq.Context(), chi.RouteCtxKey, delRctx))
	delReq = withUserCtx(delReq, "admin")
	delRec := httptest.NewRecorder()

	h.deleteRule(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("deleteRule code = %d, want 200 (body: %s)", delRec.Code, delRec.Body.String())
	}

	// Verify WebSocket envelope was automatically dispatched with empty domain list (RESTORE ACCESS)
	if len(hub.sent) != 1 {
		t.Fatalf("hub received %d messages after deleteRule, want 1", len(hub.sent))
	}
	if err := json.Unmarshal(hub.sent[0], &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Payload.BlockedDomains) != 0 {
		t.Errorf("blocked_domains after delete = %v, want empty [] to restore normal access", env.Payload.BlockedDomains)
	}
}

func TestAutoDispatchOnPolicyDeleteRestoresAccess(t *testing.T) {
	h, repo, hub := newDispatchFixture(t)
	ctx := context.Background()

	pol := &FilterPolicy{
		Name:       "Policy To Delete",
		TargetType: "device",
		TargetID:   "dev-online",
		IsEnabled:  true,
		Priority:   50,
		CreatedBy:  "admin",
	}
	if err := repo.CreatePolicy(ctx, pol); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddRule(ctx, &FilterRule{
		PolicyID: pol.ID, RuleType: "domain", Pattern: "detik.com", Action: "block", Category: "custom",
	}); err != nil {
		t.Fatal(err)
	}

	hub.sent = nil
	delReq := httptest.NewRequest(http.MethodDelete, "/api/filter/policies/"+pol.ID, nil)
	delRctx := chi.NewRouteContext()
	delRctx.URLParams.Add("id", pol.ID)
	delReq = delReq.WithContext(context.WithValue(delReq.Context(), chi.RouteCtxKey, delRctx))
	delReq = withUserCtx(delReq, "admin")
	delRec := httptest.NewRecorder()

	h.deletePolicy(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("deletePolicy code = %d, want 200", delRec.Code)
	}

	if len(hub.sent) != 1 {
		t.Fatalf("hub received %d messages after deletePolicy, want 1", len(hub.sent))
	}
	var env struct {
		Command string `json:"command"`
		Payload struct {
			BlockedDomains []string `json:"blocked_domains"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(hub.sent[0], &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Payload.BlockedDomains) != 0 {
		t.Errorf("blocked_domains after policy delete = %v, want empty [] to restore normal access", env.Payload.BlockedDomains)
	}
}

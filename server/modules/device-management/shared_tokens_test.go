package devicemanagement

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
)

type tokenRoutes struct {
	r     http.Handler
	admin string
	tech  string
}

func newTokenRoutes(t *testing.T) (*tokenRoutes, *Repository) {
	t.Helper()
	d := newExpiryDB(t)
	repo := NewRepository(d)
	jwt := auth.NewJWTService("secret-for-token-routes-0123456789", time.Hour, 24*time.Hour)
	r := chi.NewRouter()
	NewHandler(repo, d, jwt, 30*time.Minute).Register(r)
	NewEnrollmentHandler(repo, d).Register(r)
	admin, err := jwt.Issue("u-admin", "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	tech, err := jwt.Issue("u-tech", "tech", "technician")
	if err != nil {
		t.Fatal(err)
	}
	return &tokenRoutes{r: r, admin: admin.AccessToken, tech: tech.AccessToken}, repo
}

func (tr *tokenRoutes) do(t *testing.T, method, path, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	tr.r.ServeHTTP(rec, req)
	return rec
}

// Nothing read use_count or revoked_at: an operator could not see how many
// machines a batch token had enrolled, tell a used-up token from a broken
// one, or stop a leaked token before it expired.
func TestSharedTokensCanBeListedAndRevoked(t *testing.T) {
	tr, _ := newTokenRoutes(t)

	rec := tr.do(t, http.MethodPost, "/api/devices/enroll-token", tr.admin, `{"is_multi_use":true,"name":"Branch A","max_uses":5}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created createEnrollTokenResp
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if rec := enrollWith(t, tr.r, created.EnrollmentToken, "PC-1"); rec.Code != http.StatusOK {
		t.Fatalf("enroll = %d %s", rec.Code, rec.Body.String())
	}

	rec = tr.do(t, http.MethodGet, "/api/devices/enroll-tokens", tr.admin, "")
	var list []sharedTokenDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || len(list) != 1 || list[0].UseCount != 1 || list[0].MaxUses != 5 || list[0].Status != "active" {
		t.Fatalf("list = %d %s, want one active token used 1 of 5", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), created.EnrollmentToken) {
		t.Fatal("the list returned the token itself")
	}

	if rec := tr.do(t, http.MethodGet, "/api/devices/enroll-tokens", tr.tech, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("technician list = %d, want 403", rec.Code)
	}
	if rec := tr.do(t, http.MethodPost, "/api/devices/enroll-tokens/"+created.TokenID+"/revoke", tr.tech, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("technician revoke = %d, want 403", rec.Code)
	}

	rec = tr.do(t, http.MethodPost, "/api/devices/enroll-tokens/"+created.TokenID+"/revoke", tr.admin, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"revoked"`) {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	if rec := tr.do(t, http.MethodPost, "/api/devices/enroll-tokens/"+created.TokenID+"/revoke", tr.admin, ""); rec.Code != http.StatusConflict {
		t.Fatalf("second revoke = %d, want 409", rec.Code)
	}
	rec = enrollWith(t, tr.r, created.EnrollmentToken, "PC-2")
	if rec.Code != http.StatusUnauthorized || refusalCode(t, rec) != EnrollTokenRevoked {
		t.Fatalf("enroll with a revoked token = %d %s, want 401 token_revoked", rec.Code, rec.Body.String())
	}

	rec = tr.do(t, http.MethodGet, "/api/devices/enroll-tokens/"+created.TokenID, tr.admin, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"use_count":1`) {
		t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
	}
	if rec := tr.do(t, http.MethodGet, "/api/devices/enroll-tokens/nope", tr.admin, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get unknown = %d, want 404", rec.Code)
	}
}

func TestSharedTokenStatus(t *testing.T) {
	now := time.Now().UTC()
	revoked := now
	for want, tok := range map[string]EnrollmentTokenRecord{
		"active":  {ExpiresAt: now.Add(time.Hour), MaxUses: 0, UseCount: 9},
		"used_up": {ExpiresAt: now.Add(time.Hour), MaxUses: 2, UseCount: 2},
		"expired": {ExpiresAt: now.Add(-time.Second)},
		"revoked": {ExpiresAt: now.Add(time.Hour), RevokedAt: &revoked},
	} {
		if got := tok.Status(now); got != want {
			t.Errorf("status = %q, want %q", got, want)
		}
	}
}

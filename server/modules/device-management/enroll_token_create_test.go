package devicemanagement

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func createToken(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.createEnrollToken(rec, httptest.NewRequest(http.MethodPost, "/api/devices/enroll-token", strings.NewReader(body)))
	return rec
}

// Through the handler, the way the console and API callers create tokens.
func TestSharedTokenCreationDefaults(t *testing.T) {
	d := newExpiryDB(t)
	h := NewHandler(NewRepository(d), d, nil, 30*time.Minute)

	rec := createToken(t, h, `{"is_multi_use":true,"name":"batch"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var resp createEnrollTokenResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	// Without max_uses a multi-use token was stored with exactly one use.
	if resp.MaxUses != 0 {
		t.Fatalf("max_uses = %d, want 0 (unlimited) when the request leaves it out", resp.MaxUses)
	}
	// Without an expiry it lasted the 30-minute one-device window.
	if left := time.Until(resp.ExpiresAt); left < 23*time.Hour {
		t.Fatalf("expires in %v, want about 24h for a shared token", left)
	}
}

func TestTokenCreationRefusesUnusableValues(t *testing.T) {
	d := newExpiryDB(t)
	h := NewHandler(NewRepository(d), d, nil, 30*time.Minute)
	for body, code := range map[string]string{
		`{"is_multi_use":true,"max_uses":-1}`:                  "invalid_max_uses",
		`{"is_multi_use":true,"expires_in_hours":3000000}`:     "invalid_expiry",
		`{"is_multi_use":true,"duration_seconds":99999999999}`: "invalid_expiry",
	} {
		rec := createToken(t, h, body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), code) {
			t.Errorf("%s -> %d %s, want 400 %s", body, rec.Code, rec.Body.String(), code)
		}
	}
}

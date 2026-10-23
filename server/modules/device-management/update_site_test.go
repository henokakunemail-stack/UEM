package devicemanagement

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
)

func patchSite(t *testing.T, h *Handler, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/devices/"+id, bytes.NewReader([]byte(body)))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "admin"))
	rec := httptest.NewRecorder()
	h.updateDevice(rec, req)
	return rec
}

// The console now sets the site of many devices at once from what an operator
// types. A stray space made " HQ" a site of its own next to "HQ", and nothing
// bounded the length of a label every filter and report shows.
func TestSettingASiteTrimsAndBoundsIt(t *testing.T) {
	d := newExpiryDB(t)
	repo := NewRepository(d)
	h := NewHandler(repo, d, auth.NewJWTService("secret", time.Hour, 24*time.Hour), time.Hour)
	now := time.Now().UTC()
	if err := repo.Create(context.Background(), Device{ID: "dev-1", Hostname: "PC-1", OSName: "windows",
		Status: StatusOnline, EnrolledAt: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	if rec := patchSite(t, h, "dev-1", `{"site": "  HQ  "}`); rec.Code != http.StatusOK {
		t.Fatalf("set site = %d %s", rec.Code, rec.Body.String())
	}
	dev, _ := repo.GetByID(context.Background(), "dev-1")
	if dev.SiteValue() != "HQ" {
		t.Errorf("site stored as %q, want %q", dev.SiteValue(), "HQ")
	}

	for _, body := range []string{`{"site": "` + strings.Repeat("x", 65) + `"}`, `{"site": "` + UnassignedSite + `"}`, `{"site": "   "}`} {
		if rec := patchSite(t, h, "dev-1", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%.40s... = %d, want 400", body, rec.Code)
		}
	}
	dev, _ = repo.GetByID(context.Background(), "dev-1")
	if dev.SiteValue() != "HQ" {
		t.Errorf("a refused update changed the site to %q", dev.SiteValue())
	}
}

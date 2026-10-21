package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

// An enrollment row that never checked in is not an offline device. It was
// counted as one and pulled the online percentage down.
func TestPendingEnrollmentsAreNotOfflineDevices(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	seedDevice(t, d, "on", "ON", "windows", "hq", "online", &now, nil)
	seedDevice(t, d, "off", "OFF", "windows", "hq", "offline", &now, nil)
	seedDevice(t, d, "pend", "Pending Enrollment", "windows", "", "offline", nil, nil)
	d.MustExec(`UPDATE devices SET enrollment_token_hash = 'tok' WHERE id = 'pend'`)

	s, err := NewRepository(d).GetSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.TotalDevices != 2 || s.OfflineDevices != 1 || s.PendingDevices != 1 || s.OnlinePct != 50 {
		t.Fatalf("summary = total %d offline %d pending %d online%% %v, want 2 1 1 50",
			s.TotalDevices, s.OfflineDevices, s.PendingDevices, s.OnlinePct)
	}
	sites, err := NewRepository(d).GetSiteMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range sites {
		if m.Site == "unassigned" {
			t.Fatalf("the pending row appears as a site device: %+v", sites)
		}
	}
	osm, err := NewRepository(d).GetOSMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(osm) != 1 || osm[0].Count != 2 {
		t.Fatalf("os metrics = %+v, want 2 windows devices (the pending row is not one)", osm)
	}
}

// The health-alert card was the length of a list capped at 15 + 15.
func TestAlertsTotalCountsEveryAlert(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	for i := 0; i < 40; i++ {
		id := "d" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		seedDevice(t, d, id, id, "windows", "hq", "online", &now, nil)
		seedInventory(t, d, id, 3)
	}
	s, err := NewRepository(d).GetSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.AlertsTotal != 40 {
		t.Fatalf("alerts_total = %d, want 40", s.AlertsTotal)
	}
	alerts, err := NewRepository(d).GetAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if alerts[0].Value == nil || *alerts[0].Value != 3 {
		t.Fatalf("disk alert value = %v, want 3", alerts[0].Value)
	}
}

// A NULL site and an empty site were two different "sites".
func TestUnassignedSitesAreOneGroup(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	seedDevice(t, d, "a", "A", "windows", "", "online", &now, nil)
	d.MustExec(`INSERT INTO devices (id, hostname, os_name, status, last_seen_at, enrolled_at, device_secret_hash, created_at, updated_at)
		VALUES ('b','B','windows','online',?,?,'h',?,?)`, now, now, now, now)
	sites, err := NewRepository(d).GetSiteMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].Site != "unassigned" || sites[0].Total != 2 {
		t.Fatalf("sites = %+v, want one unassigned group of 2", sites)
	}
}

// A failed sub-query became 0, which read as healthy and was cached.
func TestAFailedSummaryIsAnErrorAndIsNotCached(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	seedDevice(t, d, "on", "ON", "windows", "hq", "online", &now, nil)
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(rbac.WithRole(r.Context(), rbac.RoleViewer)))
		})
	}
	r := chi.NewRouter()
	NewHandler(NewRepository(d), auth).WithCacheTTL(time.Minute).Register(r)
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/dashboard/summary", nil))
		return rec
	}

	d.MustExec(`ALTER TABLE device_inventory RENAME TO device_inventory_gone`)
	if rec := get(); rec.Code != http.StatusInternalServerError {
		t.Fatalf("summary with a failing sub-query = %d %s, want 500", rec.Code, rec.Body.String())
	}
	d.MustExec(`ALTER TABLE device_inventory_gone RENAME TO device_inventory`)
	rec := get()
	var s Summary
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	if rec.Code != http.StatusOK || s.TotalDevices != 1 {
		t.Fatalf("summary after recovery = %d %+v, want a fresh 200", rec.Code, s)
	}
}

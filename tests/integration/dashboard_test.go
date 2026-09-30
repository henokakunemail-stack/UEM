package integration

import (
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
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/dashboard"
	"github.com/jmoiron/sqlx"
)

func newDashboardEnv(t *testing.T) (*httptest.Server, *sqlx.DB, *auth.JWTService) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "dash-e2e.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	jwtSvc := auth.NewJWTService("dash-e2e-secret-0123456789abcdef", time.Minute, time.Hour)
	dashRepo := dashboard.NewRepository(d)
	dashH := dashboard.NewHandler(dashRepo, jwtSvc.RequireAuth)

	r := chi.NewRouter()
	loginH := auth.NewLoginHandler(d, jwtSvc)
	loginH.Register(r)
	dashH.Register(r)

	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close() })
	return srv, d, jwtSvc
}

func TestE2EDashboardAPI(t *testing.T) {
	srv, d, jwtSvc := newDashboardEnv(t)
	ctx := context.Background()

	// Seed viewer user
	viewerPass, _ := auth.HashPassword("viewer12345")
	_, err := d.ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, role, created_at, updated_at)
		VALUES ('u-viewer', 'viewerbob', ?, ?, ?, ?)`,
		viewerPass, rbac.RoleViewer, time.Now().UTC(), time.Now().UTC())
	if err != nil {
		t.Fatalf("seed viewer: %v", err)
	}

	pair, err := jwtSvc.Issue("u-viewer", "viewerbob", rbac.RoleViewer)
	if err != nil {
		t.Fatalf("issue viewer token: %v", err)
	}
	viewerToken := pair.AccessToken

	// Seed fleet: 2 devices in HQ, 1 in Surabaya
	now := time.Now().UTC()
	_, err = d.ExecContext(ctx, `
		INSERT INTO devices (id, hostname, os_name, status, last_seen_at, enrolled_at, device_secret_hash, site, created_at, updated_at)
		VALUES
		('d1', 'PC-HQ-01', 'windows', 'online', ?, ?, 'h1', 'hq', ?, ?),
		('d2', 'PC-HQ-02', 'windows', 'offline', ?, ?, 'h2', 'hq', ?, ?),
		('d3', 'SRV-SBY-01', 'linux', 'online', ?, ?, 'h3', 'surabaya', ?, ?)`,
		now, now, now, now,
		now, now, now, now,
		now, now, now, now)
	if err != nil {
		t.Fatalf("seed devices: %v", err)
	}

	// Seed low disk inventory on d2 (8.5% < 15%)
	_, err = d.ExecContext(ctx, `
		INSERT INTO device_inventory (id, device_id, hw, software, os_detail, hw_disk_free_pct, collected_at, updated_at)
		VALUES ('inv-d2', 'd2', '{}', '[]', '{}', 8.5, ?, ?)`,
		now, now)
	if err != nil {
		t.Fatalf("seed inventory: %v", err)
	}

	// 1. Unauthenticated request to /api/dashboard/summary should return 401
	resp, err := http.Get(srv.URL + "/api/dashboard/summary")
	if err != nil {
		t.Fatalf("get summary without auth: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized without token, got %d", resp.StatusCode)
	}

	// 2. Authenticated request as viewer to /api/dashboard/summary
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/dashboard/summary", nil)
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get summary: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var sum dashboard.Summary
	if err := json.NewDecoder(resp.Body).Decode(&sum); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if sum.TotalDevices != 3 {
		t.Errorf("expected total_devices 3, got %d", sum.TotalDevices)
	}
	if sum.OnlineDevices != 2 {
		t.Errorf("expected online_devices 2, got %d", sum.OnlineDevices)
	}
	if sum.OfflineDevices != 1 {
		t.Errorf("expected offline_devices 1, got %d", sum.OfflineDevices)
	}
	if sum.LowDiskAlerts != 1 {
		t.Errorf("expected low_disk_alerts 1, got %d", sum.LowDiskAlerts)
	}
	if sum.SitesCount != 2 {
		t.Errorf("expected sites_count 2 (hq, surabaya), got %d", sum.SitesCount)
	}

	// 3. Authenticated request to /api/dashboard/sites
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/dashboard/sites", nil)
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get sites: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var sites []dashboard.SiteMetric
	if err := json.NewDecoder(resp.Body).Decode(&sites); err != nil {
		t.Fatalf("decode sites: %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("expected 2 sites, got %d", len(sites))
	}

	// 4. Authenticated request to /api/dashboard/alerts
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/dashboard/alerts", nil)
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get alerts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var alerts []dashboard.Alert
	if err := json.NewDecoder(resp.Body).Decode(&alerts); err != nil {
		t.Fatalf("decode alerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert for low disk, got %d", len(alerts))
	}
	if alerts[0].Type != "low_disk" || alerts[0].DeviceID != "d2" {
		t.Errorf("unexpected alert: %+v", alerts[0])
	}
}

package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/jmoiron/sqlx"
)

func newTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedDevice(t *testing.T, d *sqlx.DB, id, hostname, os, site, status string, lastSeen *time.Time, retired *time.Time) {
	t.Helper()
	now := time.Now().UTC()
	_, err := d.Exec(`
		INSERT INTO devices (id, hostname, os_name, status, last_seen_at, enrolled_at,
		                     device_secret_hash, site, created_at, updated_at, retired_at)
		VALUES (?, ?, ?, ?, ?, ?, 'secret_hash', ?, ?, ?, ?)`,
		id, hostname, os, status, lastSeen, now, site, now, now, retired)
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

func seedInventory(t *testing.T, d *sqlx.DB, devID string, diskFreePct float64) {
	t.Helper()
	now := time.Now().UTC()
	_, err := d.Exec(`
		INSERT INTO device_inventory (id, device_id, hw, software, os_detail, hw_disk_free_pct, collected_at, updated_at)
		VALUES (?, ?, '{}', '[]', '{}', ?, ?, ?)`,
		"inv-"+devID, devID, diskFreePct, now, now)
	if err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
}

func TestDashboardSummaryEmpty(t *testing.T) {
	d := newTestDB(t)
	repo := NewRepository(d)
	ctx := context.Background()

	s, err := repo.GetSummary(ctx)
	if err != nil {
		t.Fatalf("GetSummary failed: %v", err)
	}
	if s.TotalDevices != 0 || s.OnlineDevices != 0 || s.OfflineDevices != 0 || s.RetiredDevices != 0 {
		t.Errorf("expected all 0 counts, got %+v", s)
	}
	if s.OnlinePct != 0.0 {
		t.Errorf("expected 0.0%% online, got %.1f", s.OnlinePct)
	}
}

func TestDashboardSummaryWithFleet(t *testing.T) {
	d := newTestDB(t)
	repo := NewRepository(d)
	ctx := context.Background()

	now := time.Now().UTC()
	oldTime := now.Add(-48 * time.Hour)
	retiredTime := now.Add(-1 * time.Hour)

	// Seed 3 online devices, 1 offline device, 1 retired device
	seedDevice(t, d, "d1", "PC-HQ-01", "windows", "hq", "online", &now, nil)
	seedDevice(t, d, "d2", "PC-HQ-02", "windows", "hq", "online", &now, nil)
	seedDevice(t, d, "d3", "SRV-SBY-01", "linux", "surabaya", "online", &now, nil)
	seedDevice(t, d, "d4", "MAC-MDN-01", "macos", "medan", "offline", &oldTime, nil)
	seedDevice(t, d, "d5", "PC-OLD-01", "windows", "hq", "offline", &oldTime, &retiredTime)

	// Seed inventory with low disk alert on d1 (10.5% < 15%)
	seedInventory(t, d, "d1", 10.5)
	seedInventory(t, d, "d2", 45.0)

	s, err := repo.GetSummary(ctx)
	if err != nil {
		t.Fatalf("GetSummary failed: %v", err)
	}

	if s.TotalDevices != 4 { // non-retired
		t.Errorf("expected total 4, got %d", s.TotalDevices)
	}
	if s.OnlineDevices != 3 {
		t.Errorf("expected online 3, got %d", s.OnlineDevices)
	}
	if s.OfflineDevices != 1 {
		t.Errorf("expected offline 1, got %d", s.OfflineDevices)
	}
	if s.RetiredDevices != 1 {
		t.Errorf("expected retired 1, got %d", s.RetiredDevices)
	}
	if s.OnlinePct != 75.0 {
		t.Errorf("expected online_pct 75.0, got %.1f", s.OnlinePct)
	}
	if s.LowDiskAlerts != 1 {
		t.Errorf("expected 1 low disk alert, got %d", s.LowDiskAlerts)
	}
	if s.SitesCount != 3 { // hq, surabaya, medan
		t.Errorf("expected 3 sites, got %d", s.SitesCount)
	}
}

func TestDashboardSiteMetrics(t *testing.T) {
	d := newTestDB(t)
	repo := NewRepository(d)
	ctx := context.Background()

	now := time.Now().UTC()
	seedDevice(t, d, "d1", "PC-HQ-01", "windows", "hq", "online", &now, nil)
	seedDevice(t, d, "d2", "PC-HQ-02", "windows", "hq", "offline", &now, nil)
	seedDevice(t, d, "d3", "PC-SBY-01", "windows", "surabaya", "online", &now, nil)

	sites, err := repo.GetSiteMetrics(ctx)
	if err != nil {
		t.Fatalf("GetSiteMetrics failed: %v", err)
	}

	if len(sites) != 2 {
		t.Fatalf("expected 2 sites, got %d", len(sites))
	}
	if sites[0].Site != "hq" || sites[0].Total != 2 || sites[0].Online != 1 || sites[0].Offline != 1 {
		t.Errorf("unexpected hq metric: %+v", sites[0])
	}
	if sites[0].OnlinePct != 50.0 {
		t.Errorf("expected 50.0%% online for hq, got %.1f", sites[0].OnlinePct)
	}
	if sites[1].Site != "surabaya" || sites[1].Total != 1 || sites[1].Online != 1 {
		t.Errorf("unexpected surabaya metric: %+v", sites[1])
	}
}

func TestDashboardOSMetrics(t *testing.T) {
	d := newTestDB(t)
	repo := NewRepository(d)
	ctx := context.Background()

	now := time.Now().UTC()
	seedDevice(t, d, "d1", "PC-1", "windows", "hq", "online", &now, nil)
	seedDevice(t, d, "d2", "PC-2", "windows", "hq", "online", &now, nil)
	seedDevice(t, d, "d3", "PC-3", "linux", "hq", "online", &now, nil)
	seedDevice(t, d, "d4", "PC-4", "macos", "hq", "online", &now, nil)

	osMetrics, err := repo.GetOSMetrics(ctx)
	if err != nil {
		t.Fatalf("GetOSMetrics failed: %v", err)
	}

	if len(osMetrics) != 3 {
		t.Fatalf("expected 3 OS metrics, got %d", len(osMetrics))
	}
	if osMetrics[0].OSName != "windows" || osMetrics[0].Count != 2 || osMetrics[0].Pct != 50.0 {
		t.Errorf("unexpected windows metric: %+v", osMetrics[0])
	}
}

func TestDashboardAlerts(t *testing.T) {
	d := newTestDB(t)
	repo := NewRepository(d)
	ctx := context.Background()

	now := time.Now().UTC()
	oldTime := now.Add(-48 * time.Hour) // > 24h offline
	seedDevice(t, d, "d1", "PC-CRIT-DISK", "windows", "hq", "online", &now, nil)
	seedDevice(t, d, "d2", "PC-LONG-OFFLINE", "linux", "surabaya", "offline", &oldTime, nil)

	seedInventory(t, d, "d1", 4.2) // Critical < 5%

	alerts, err := repo.GetAlerts(ctx)
	if err != nil {
		t.Fatalf("GetAlerts failed: %v", err)
	}

	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(alerts))
	}

	// Verify low disk alert
	hasDiskAlert := false
	for _, a := range alerts {
		if a.Type == "low_disk" {
			hasDiskAlert = true
			if a.Severity != "critical" {
				t.Errorf("expected critical severity for 4.2%% disk, got %s", a.Severity)
			}
			if a.DeviceID != "d1" {
				t.Errorf("expected device d1, got %s", a.DeviceID)
			}
		}
	}
	if !hasDiskAlert {
		t.Errorf("expected low_disk alert in %+v", alerts)
	}
}

func TestDashboardHTTPHandlers(t *testing.T) {
	d := newTestDB(t)
	repo := NewRepository(d)

	testAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := rbac.WithRole(r.Context(), rbac.RoleViewer)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	h := NewHandler(repo, testAuth)

	r := chi.NewRouter()
	h.Register(r)

	now := time.Now().UTC()
	seedDevice(t, d, "d1", "PC-HQ-01", "windows", "hq", "online", &now, nil)

	// 1. GET /api/dashboard/summary
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/summary", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var s Summary
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if s.TotalDevices != 1 || s.OnlineDevices != 1 {
		t.Errorf("unexpected summary: %+v", s)
	}

	// 2. GET /api/dashboard/sites
	req = httptest.NewRequest(http.MethodGet, "/api/dashboard/sites", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// 3. GET /api/dashboard/os
	req = httptest.NewRequest(http.MethodGet, "/api/dashboard/os", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// 4. GET /api/dashboard/alerts
	req = httptest.NewRequest(http.MethodGet, "/api/dashboard/alerts", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// 5. GET /api/dashboard/activity
	req = httptest.NewRequest(http.MethodGet, "/api/dashboard/activity?limit=5", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

func opsServer(t *testing.T, metricsToken string) (http.Handler, *serverApp) {
	t.Helper()
	cfg := testConfig(t)
	cfg.MetricsToken = metricsToken
	database, err := db.Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	srv, app := buildServer(cfg, database)
	t.Cleanup(app.teardown)
	return srv.Handler, app
}

func get(h http.Handler, path string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A node shutting down fails readiness before it closes anything, so the load
// balancer stops routing to it first; liveness keeps answering, because the
// process is still up and restarting it would only make the drain worse.
func TestReadinessFailsWhileDraining(t *testing.T) {
	h, app := opsServer(t, "")
	if rec := get(h, "/readyz", nil); rec.Code != http.StatusOK {
		t.Fatalf("/readyz before shutdown: %d %s", rec.Code, rec.Body)
	}
	app.draining.Store(true)
	rec := get(h, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "draining") {
		t.Fatalf("/readyz while draining: %d %s, want 503 draining", rec.Code, rec.Body)
	}
	if rec := get(h, "/healthz", nil); rec.Code != http.StatusOK {
		t.Fatalf("/healthz while draining: %d", rec.Code)
	}
}

func TestHealthReportsNodeAndVersion(t *testing.T) {
	h, _ := opsServer(t, "")
	var body map[string]any
	if err := json.Unmarshal(get(h, "/healthz", nil).Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"version", "node", "uptime_seconds", "leader"} {
		if _, ok := body[k]; !ok {
			t.Errorf("/healthz has no %q: %v", k, body)
		}
	}
}

// /metrics serves the Prometheus format, and with METRICS_TOKEN set nothing
// without the token.
func TestMetricsEndpointAndToken(t *testing.T) {
	h, _ := opsServer(t, "s3cret-token")
	get(h, "/healthz", nil) // something for the request counter to count

	if rec := get(h, "/metrics", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("/metrics without token: %d, want 401", rec.Code)
	}
	if rec := get(h, "/metrics", map[string]string{"Authorization": "Bearer wrong"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("/metrics with a wrong token: %d, want 401", rec.Code)
	}
	rec := get(h, "/metrics", map[string]string{"Authorization": "Bearer s3cret-token"})
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics with the token: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"em_build_info{", "em_agents_connected ", "em_leader ", "em_db_open_connections ",
		`em_http_requests_total{method="GET",route="/healthz",status="200"}`,
		`em_relays_active{kind="remote_control"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics has no %q", want)
		}
	}
}

func TestStatementTimeoutIsAddedToPostgresURLsOnly(t *testing.T) {
	got := withStatementTimeout("postgres://u@h/db?sslmode=disable", 30*time.Second)
	if !strings.Contains(got, "statement_timeout%3D30000") || !strings.Contains(got, "sslmode=disable") {
		t.Fatalf("postgres URL: %s", got)
	}
	for _, dsn := range []string{"data/x.db", "postgres://u@h/db?options=-c%20statement_timeout%3D5"} {
		if got := withStatementTimeout(dsn, time.Second); got != dsn {
			t.Fatalf("%s rewritten to %s", dsn, got)
		}
	}
	if got := withStatementTimeout("postgres://u@h/db", 0); got != "postgres://u@h/db" {
		t.Fatalf("zero timeout rewrote the URL: %s", got)
	}
}

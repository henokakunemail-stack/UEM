package alerting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
)

type alertFixture struct {
	h     *Handler
	e     *Evaluator
	db    *sqlx.DB
	audit *stubAudit
}

func newAlertFixture(t *testing.T) *alertFixture {
	t.Helper()
	h, database := newRuleFixture(t)
	audit := &stubAudit{}
	h.audit = audit
	h.evaluator.WithAuditor(audit)
	return &alertFixture{h: h, e: h.evaluator, db: database, audit: audit}
}

func (f *alertFixture) device(t *testing.T, id, status string, lastSeen *time.Time) {
	t.Helper()
	now := time.Now().UTC()
	f.db.MustExec(`INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
		device_secret_hash, enrolled_at, created_at, updated_at, last_seen_at)
		VALUES (?, ?, 'windows', '11', '1.0.0', ?, 'x', ?, ?, ?, ?)`, id, id, status, now, now, now, lastSeen)
}

func (f *alertFixture) disk(t *testing.T, id string, free float64) {
	t.Helper()
	now := time.Now().UTC()
	f.db.MustExec(`INSERT INTO device_inventory (device_id, hw, software, os_detail, hw_disk_free_pct, collected_at, updated_at)
		VALUES (?, '{}', '[]', '{}', ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET hw_disk_free_pct = excluded.hw_disk_free_pct`, id, free, now, now)
}

func (f *alertFixture) rule(t *testing.T, id, typ string, threshold float64, webhook string) {
	t.Helper()
	now := time.Now().UTC()
	f.db.MustExec(`INSERT INTO alert_rules (id, name, rule_type, threshold_val, severity, webhook_url, is_enabled, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'warning', ?, 1, 'u1', ?, ?)`, id, id, typ, threshold, webhook, now, now)
}

func (f *alertFixture) incidentStatus(t *testing.T, ruleID, deviceID string) (status, resolvedBy string) {
	t.Helper()
	var row struct {
		Status string  `db:"status"`
		By     *string `db:"resolved_by"`
	}
	if err := f.db.Get(&row, `SELECT status, resolved_by FROM alert_incidents WHERE rule_id = ? AND device_id = ?
		ORDER BY first_triggered_at DESC LIMIT 1`, ruleID, deviceID); err != nil {
		t.Fatal(err)
	}
	if row.By != nil {
		resolvedBy = *row.By
	}
	return row.Status, resolvedBy
}

func (f *alertFixture) eval(t *testing.T) *EvalResult {
	t.Helper()
	res, err := f.e.EvaluateAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// An incident whose condition cleared stayed open forever: nothing closed
// incidents but an operator.
func TestAnIncidentClosesWhenItsConditionClears(t *testing.T) {
	f := newAlertFixture(t)
	f.device(t, "d1", "online", nil)
	f.disk(t, "d1", 5)
	f.rule(t, "r-disk", "disk_low", 15, "")

	f.eval(t)
	if s, _ := f.incidentStatus(t, "r-disk", "d1"); s != "open" {
		t.Fatalf("incident with 5%% free = %q, want open", s)
	}
	var value float64
	_ = f.db.Get(&value, `SELECT value FROM alert_incidents WHERE rule_id = 'r-disk'`)
	if value != 5 {
		t.Fatalf("stored value = %v, want 5 (the measured free space)", value)
	}

	f.disk(t, "d1", 60)
	res := f.eval(t)
	status, by := f.incidentStatus(t, "r-disk", "d1")
	if status != "resolved" || by != SystemActor || res.IncidentsResolved != 1 {
		t.Fatalf("after cleanup: status %q by %q, resolved %d; want resolved by system", status, by, res.IncidentsResolved)
	}
	if f.audit.calls == 0 {
		t.Fatal("the system resolution was not audited")
	}
}

// A query that fails says nothing about the fleet. Closing incidents on it
// would report every device healthy during a database hiccup.
func TestAFailedQueryResolvesNothing(t *testing.T) {
	f := newAlertFixture(t)
	f.device(t, "d1", "online", nil)
	f.disk(t, "d1", 5)
	f.rule(t, "r-disk", "disk_low", 15, "")
	f.eval(t)

	f.db.MustExec(`ALTER TABLE device_inventory RENAME TO device_inventory_gone`)
	t.Cleanup(func() { f.db.MustExec(`ALTER TABLE device_inventory_gone RENAME TO device_inventory`) })
	f.eval(t)
	if s, _ := f.incidentStatus(t, "r-disk", "d1"); s != "open" {
		t.Fatalf("incident after a failed evaluation = %q, want it left open", s)
	}
}

// The offline threshold was ignored and never-seen pending rows fired.
func TestOfflineHonoursItsThresholdAndIgnoresPendingRows(t *testing.T) {
	f := newAlertFixture(t)
	recent := time.Now().UTC().Add(-5 * time.Minute)
	long := time.Now().UTC().Add(-20 * time.Minute)
	f.device(t, "napping", "offline", &recent)
	f.device(t, "gone", "offline", &long)
	f.device(t, "pending", "offline", nil)
	f.rule(t, "r-off", "device_offline", 15, "")

	f.eval(t)
	var devices []string
	if err := f.db.Select(&devices, `SELECT device_id FROM alert_incidents WHERE rule_id = 'r-off'`); err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0] != "gone" {
		t.Fatalf("offline incidents for %v, want only [gone]", devices)
	}
}

// Disabling a rule stops it watching, so its incidents could never clear on
// their own; they are closed when it is disabled.
func TestDisablingARuleClosesItsIncidents(t *testing.T) {
	f := newAlertFixture(t)
	f.device(t, "d1", "online", nil)
	f.disk(t, "d1", 5)
	f.rule(t, "r-disk", "disk_low", 15, "")
	f.eval(t)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "r-disk")
	req := httptest.NewRequest(http.MethodPut, "/api/alerts/rules/r-disk", strings.NewReader(`{"is_enabled":false}`))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	f.h.updateRule(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body.String())
	}
	if s, _ := f.incidentStatus(t, "r-disk", "d1"); s != "resolved" {
		t.Fatalf("incident of a disabled rule = %q, want resolved", s)
	}
}

// Receivers only ever heard about incidents opening.
func TestTheWebhookHearsAboutResolution(t *testing.T) {
	var mu sync.Mutex
	var events []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p struct {
			Event string `json:"event"`
		}
		_ = json.Unmarshal(b, &p)
		mu.Lock()
		events = append(events, p.Event)
		mu.Unlock()
	}))
	defer srv.Close()

	f := newAlertFixture(t)
	f.e.httpClient = srv.Client() // the production client refuses loopback
	f.device(t, "d1", "online", nil)
	f.disk(t, "d1", 5)
	f.rule(t, "r-disk", "disk_low", 15, srv.URL)
	f.eval(t)
	// Deliveries are asynchronous; let the first land before the second.
	f.e.WaitWebhooks(5 * time.Second)
	f.disk(t, "d1", 60)
	f.eval(t)
	f.e.WaitWebhooks(5 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	if strings.Join(events, ",") != "alert.incident,alert.resolved" {
		t.Fatalf("webhook events = %v, want [alert.incident alert.resolved]", events)
	}
}

// The Open card was the length of a list capped at 100.
func TestIncidentCountsAreNotCappedByTheList(t *testing.T) {
	f := newAlertFixture(t)
	f.rule(t, "r-disk", "disk_low", 15, "")
	now := time.Now().UTC()
	for i := 0; i < 150; i++ {
		id := NewID()
		f.device(t, id, "online", nil)
		f.db.MustExec(`INSERT INTO alert_incidents (id, rule_id, device_id, severity, title, message, status, trigger_count, first_triggered_at, last_triggered_at)
			VALUES (?, 'r-disk', ?, 'warning', 't', 'm', 'open', 1, ?, ?)`, NewID(), id, now, now)
	}
	rec := httptest.NewRecorder()
	f.h.countIncidents(rec, httptest.NewRequest(http.MethodGet, "/api/alerts/incidents/counts", nil))
	if !strings.Contains(rec.Body.String(), `"open":150`) {
		t.Fatalf("counts = %s, want open 150", rec.Body.String())
	}
}

func TestRuleValidation(t *testing.T) {
	f := newAlertFixture(t)
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		f.h.createRule(rec, httptest.NewRequest(http.MethodPost, "/api/alerts/rules", strings.NewReader(body)))
		return rec
	}
	cases := map[string]string{
		`{"name":"x","rule_type":"ram_high","threshold_val":90,"severity":"warning"}`:                        "invalid_rule_type",
		`{"name":"x","rule_type":"disk_low","threshold_val":0,"severity":"warning"}`:                         "invalid_threshold",
		`{"name":"x","rule_type":"device_offline","threshold_val":99999,"severity":"warning"}`:               "invalid_threshold",
		`{"name":"x","rule_type":"disk_low","threshold_val":10,"severity":"urgent"}`:                         "invalid_severity",
		`{"name":"x","rule_type":"disk_low","threshold_val":10,"severity":"info","webhook_url":"ftp://h/x"}`: "invalid_webhook_url",
	}
	for body, code := range cases {
		if rec := post(body); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), code) {
			t.Errorf("%s -> %d %s, want 400 %s", body, rec.Code, rec.Body.String(), code)
		}
	}
	rec := post(`{"name":"ok","rule_type":"disk_low","threshold_val":10,"severity":"info"}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"is_enabled":true`) {
		t.Fatalf("a valid rule without is_enabled = %d %s, want 201 enabled", rec.Code, rec.Body.String())
	}
}

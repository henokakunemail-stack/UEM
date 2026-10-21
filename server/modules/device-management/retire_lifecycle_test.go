package devicemanagement

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func seedRetireFixture(t *testing.T) (*inventoryHandler, *recordingAudit, func(string) string) {
	t.Helper()
	d := newTestDB(t)
	now := time.Now().UTC()
	expired := now.Add(-PendingExpiryGrace - time.Hour)
	fresh := now.Add(-time.Hour)
	seen := now.Add(-time.Minute)
	for _, row := range []struct {
		id       string
		token    any
		expires  any
		lastSeen any
	}{
		{"enrolled", nil, nil, seen},
		{"pending-old", "tok-old", expired, nil},
		{"pending-new", "tok-new", fresh, nil},
	} {
		d.MustExec(`INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status, device_secret_hash,
			enrolled_at, created_at, updated_at, enrollment_token_hash, enrollment_token_expires_at, last_seen_at)
			VALUES (?, ?, 'windows', '11', '1.0.0', 'offline', '', ?, ?, ?, ?, ?, ?)`,
			row.id, row.id, now, now, now, row.token, row.expires, row.lastSeen)
	}
	d.MustExec(`INSERT INTO alert_rules (id, name, rule_type, threshold_val, severity, webhook_url, is_enabled, created_by, created_at, updated_at)
		VALUES ('r1','r','device_offline',15,'warning','',1,'u',?,?)`, now, now)
	d.MustExec(`INSERT INTO alert_incidents (id, rule_id, device_id, severity, title, message, status, trigger_count, first_triggered_at, last_triggered_at)
		VALUES ('i1','r1','enrolled','warning','t','m','open',1,?,?)`, now, now)

	audit := &recordingAudit{}
	h := newInventoryHandler(newInventoryRepository(d), audit, offlineHub{})
	status := func(id string) string {
		var s string
		if err := d.Get(&s, `SELECT status FROM devices WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		return s
	}
	incident := func() string {
		var s string
		_ = d.Get(&s, `SELECT status FROM alert_incidents WHERE id = 'i1'`)
		return s
	}
	_ = incident
	return h, audit, func(id string) string {
		if id == "incident" {
			return incident()
		}
		return status(id)
	}
}

// A retired device is never evaluated again, so its incidents could never
// clear; they stayed on the alerts page for a machine that was gone.
func TestRetiringADeviceResolvesItsIncidents(t *testing.T) {
	h, _, read := seedRetireFixture(t)
	if err := h.repo.retire(context.Background(), "enrolled"); err != nil {
		t.Fatal(err)
	}
	if got := read("incident"); got != "resolved" {
		t.Fatalf("incident of a retired device = %q, want resolved", got)
	}
}

// Rows for an enrollment that never happened were stored as offline devices
// and counted as such forever.
func TestExpiredPendingEnrollmentsAreRetired(t *testing.T) {
	h, audit, read := seedRetireFixture(t)
	n, err := h.RetireExpiredPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || read("pending-old") != StatusRetired {
		t.Fatalf("retired %d, pending-old = %q; want 1 and retired", n, read("pending-old"))
	}
	if read("pending-new") == StatusRetired || read("enrolled") == StatusRetired {
		t.Fatal("a fresh pending row or an enrolled device was retired")
	}
	if len(audit.calls) != 1 || audit.calls[0].action != "device.pending_expired" {
		t.Fatalf("audit = %+v, want one device.pending_expired", audit.calls)
	}
}

// The device row is gone after a delete, so an audit entry with only the id
// could never be tied back to a machine.
func TestDeletingADeviceAuditsWhatItWas(t *testing.T) {
	h, audit, _ := seedRetireFixture(t)
	if err := h.repo.retire(context.Background(), "enrolled"); err != nil {
		t.Fatal(err)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "enrolled")
	req := httptest.NewRequest(http.MethodDelete, "/api/devices/enrolled", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.deleteDevice(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	last := audit.calls[len(audit.calls)-1]
	if last.action != "device.deleted" || last.details["hostname"] != "enrolled" {
		t.Fatalf("audit = %+v, want device.deleted with the hostname", last)
	}
}

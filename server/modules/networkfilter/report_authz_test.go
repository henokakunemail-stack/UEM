package networkfilter

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"

	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

type nopAudit struct{}

func (nopAudit) Log(context.Context, string, string, string, string, map[string]string) error {
	return nil
}

func nfFixture(t *testing.T) (*Handler, *sqlx.DB) {
	t.Helper()

	database, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	// AuthenticateAgent does a SELECT against devices, and FindBySecretHash
	// does SELECT *, so this table carries every column the Device struct scans
	// into. os_name is NOT NULL in the struct, so a NULL there surfaces as a scan
	// error that the handler reports as bad credentials -- which reads exactly
	// like an authentication failure and sends you hunting the wrong thing.
	database.MustExec(`CREATE TABLE devices (
		id TEXT PRIMARY KEY,
		hostname TEXT,
		os_name TEXT,
		os_version TEXT,
		agent_version TEXT,
		status TEXT DEFAULT 'offline',
		last_seen_at DATETIME,
		enrolled_at DATETIME NOT NULL,
		enrollment_token_hash TEXT,
		device_secret_hash TEXT NOT NULL,
		site TEXT,
		retired_at DATETIME,
		capabilities TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);`)
	database.MustExec(`CREATE TABLE device_filter_states (
		device_id TEXT PRIMARY KEY,
		policy_version TEXT,
		status TEXT,
		rules_applied INTEGER NOT NULL DEFAULT 0,
		last_applied_at DATETIME,
		error_message TEXT
	);`)

	database.MustExec(`INSERT INTO devices
		(id, hostname, os_name, device_secret_hash, enrolled_at, created_at, updated_at)
		VALUES
		('dev-a','PC-A','windows',?, '2026-01-01','2026-01-01','2026-01-01'),
		('dev-b','PC-B','windows',?, '2026-01-01','2026-01-01','2026-01-01')`,
		devicemgmt.HashToken("secret-a"), devicemgmt.HashToken("secret-b"))
	// dev-b starts out synced, so a write that should not have happened is
	// visible as a change rather than as an absence.
	database.MustExec(`INSERT INTO device_filter_states (device_id, policy_version, status, rules_applied)
		VALUES ('dev-b','v1','synced',3)`)

	return &Handler{
		repo:    NewRepository(database),
		devices: devicemgmt.NewRepository(database),
		audit:   nopAudit{},
	}, database
}

func reportFilterState(t *testing.T, h *Handler, pathID, headerID, secret string) *httptest.ResponseRecorder {
	t.Helper()
	// The status has to be a real one: agentReportFilterState rejects anything
	// outside the closed set, and a bogus value would 400 on the way in and mask
	// the ownership assertion this fixture exists to make. "failed" carries the
	// same hostile intent -- it is the worst thing an agent can claim about its
	// own enforcement -- and the invented version and rule count below already
	// mark the row as not written by a real agent.
	body, err := json.Marshal(agentFilterReportReq{
		PolicyVersion: "v666", Status: statusFailed, RulesApplied: 999,
		ErrorMessage: "written by another device",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/filter/state", bytes.NewReader(body))
	req.Header.Set("X-Device-Id", headerID)
	req.Header.Set("X-Device-Secret", secret)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", pathID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.agentReportFilterState(rec, req)
	return rec
}

func filterStateRow(t *testing.T, database *sqlx.DB, deviceID string) (version, status string, rules int) {
	t.Helper()
	if err := database.Get(&version,
		`SELECT policy_version FROM device_filter_states WHERE device_id = ?`, deviceID); err != nil {
		t.Fatal(err)
	}
	if err := database.Get(&status,
		`SELECT status FROM device_filter_states WHERE device_id = ?`, deviceID); err != nil {
		t.Fatal(err)
	}
	if err := database.Get(&rules,
		`SELECT rules_applied FROM device_filter_states WHERE device_id = ?`, deviceID); err != nil {
		t.Fatal(err)
	}
	return version, status, rules
}

// TestAnAgentCannotOverwriteAnotherDevicesFilterState is the regression test for
// the fourth instance of this bug in the codebase.
//
// agentReportFilterState called AuthenticateAgent and threw the result away: `if
// _, ok := ...`. It then took the device id from the URL path with
// chi.URLParam, and that is what it wrote. So the authentication established
// only that the caller held some valid device secret, and every subsequent
// decision was made about a different, attacker-chosen device.
//
// The written row is the one the console reads to decide whether an endpoint is
// enforcing web filtering, so the practical effect was that any enrolled agent
// in the fleet could mark any other endpoint as 'error' with an invented
// policy version -- and, in the other direction, could silence a real
// 'error' by reporting it back as synced.
func TestAnAgentCannotOverwriteAnotherDevicesFilterState(t *testing.T) {
	h, database := nfFixture(t)

	rec := reportFilterState(t, h, "dev-b", "dev-a", "secret-a")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: dev-a reported filter state for dev-b and "+
			"the server accepted it (body: %s)", rec.Code, rec.Body.String())
	}

	version, status, rules := filterStateRow(t, database, "dev-b")
	if version != "v1" || status != "synced" || rules != 3 {
		t.Errorf("dev-b's filter state is now (%s, %s, %d), want (v1, synced, 3): "+
			"another device's enforcement state was rewritten", version, status, rules)
	}
}

// TestADeviceCanStillReportItsOwnFilterState: the guard has to be ownership.
func TestADeviceCanStillReportItsOwnFilterState(t *testing.T) {
	h, database := nfFixture(t)

	rec := reportFilterState(t, h, "dev-a", "dev-a", "secret-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: dev-a could not report its own filter "+
			"state (body: %s)", rec.Code, rec.Body.String())
	}
	var version string
	if err := database.Get(&version,
		`SELECT policy_version FROM device_filter_states WHERE device_id = 'dev-a'`); err != nil {
		t.Fatal(err)
	}
	if version != "v666" {
		t.Errorf("dev-a policy_version = %q, want \"v666\"", version)
	}
}

// TestAFilterReportWithoutAPathIDStillRecords: some callers route to this
// handler with no {id} segment at all. Refusing that would be a new outage, and
// the authenticated identity is enough on its own -- the path is the untrusted
// part, never the identity.
func TestAFilterReportWithoutAPathIDStillRecords(t *testing.T) {
	h, database := nfFixture(t)

	body, _ := json.Marshal(agentFilterReportReq{PolicyVersion: "v2", Status: "synced", RulesApplied: 1})
	req := httptest.NewRequest(http.MethodPost, "/api/agent/filter/state", bytes.NewReader(body))
	req.Header.Set("X-Device-Id", "dev-a")
	req.Header.Set("X-Device-Secret", "secret-a")
	rctx := chi.NewRouteContext() // deliberately no {id}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.agentReportFilterState(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a report with no path id was refused "+
			"(body: %s)", rec.Code, rec.Body.String())
	}
	var version string
	if err := database.Get(&version,
		`SELECT policy_version FROM device_filter_states WHERE device_id = 'dev-a'`); err != nil {
		t.Fatal(err)
	}
	if version != "v2" {
		t.Errorf("dev-a policy_version = %q, want \"v2\"", version)
	}
}

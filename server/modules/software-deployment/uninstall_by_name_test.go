package softwaredeployment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// These tests cover the same failure the console has already shipped twice: an
// answer that says the operation happened when nothing did. The remote-control
// session handler answered 201 for a command its agent never received, and the
// compliance panel renders "Fully Compliant" for a device with zero scanned
// rows. Both look like success in the UI and neither is one.
//
// So each case below asserts on two things at once: the status code, and whether
// an agent_commands row was created. A refusal that still leaves a command row
// behind would queue a removal nobody asked for.

// uninstallHub records what was dispatched, and can be told to be offline or to
// drop the send. The drop is a real outcome: SendTo returns false when the
// agent's 64-slot queue is full.
type uninstallHub struct {
	online   bool
	dropSend bool
	sent     [][]byte
}

func (h *uninstallHub) Online(string) bool { return h.online }

func (h *uninstallHub) SendTo(_ string, payload []byte) bool {
	if h.dropSend {
		return false
	}
	h.sent = append(h.sent, payload)
	return true
}

type nullAuditor struct{}

func (nullAuditor) Log(context.Context, string, string, string, string, map[string]string) error {
	return nil
}

// deviceLookup answers by id, which is all the by-name route asks for.
type deviceLookup struct{ known map[string]devicemgmt.Device }

func (l deviceLookup) FindBySecretHash(context.Context, string) (devicemgmt.Device, error) {
	return devicemgmt.Device{}, context.Canceled
}

func (l deviceLookup) GetByID(_ context.Context, id string) (devicemgmt.Device, error) {
	d, ok := l.known[id]
	if !ok {
		return devicemgmt.Device{}, devicemgmt.ErrNotFound
	}
	return d, nil
}

func uninstallFixture(t *testing.T, caps string, hub *uninstallHub) (*chi.Mux, *sqlx.DB) {
	t.Helper()
	// A file database with the production DSN, for the reason documented in
	// package_handler_test.go: ":memory:" gives each pooled connection its own
	// database, so a handler on a different connection reads an empty one.
	d, err := sqlx.Open("sqlite", db.DSNForPath(filepath.Join(t.TempDir(), "uninstall.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	d.MustExec(`CREATE TABLE agent_commands (
		id TEXT PRIMARY KEY, device_id TEXT NOT NULL, command_type TEXT NOT NULL,
		payload TEXT, status TEXT NOT NULL DEFAULT 'pending',
		created_at DATETIME NOT NULL, sent_at DATETIME, completed_at DATETIME, result TEXT);`)
	d.MustExec(`CREATE TABLE devices (
		id TEXT PRIMARY KEY, hostname TEXT NOT NULL DEFAULT '',
		os_name TEXT NOT NULL DEFAULT '', site TEXT, status TEXT NOT NULL DEFAULT 'online',
		retired_at DATETIME, capabilities TEXT);`)
	d.MustExec(`INSERT INTO devices (id, hostname, os_name, capabilities)
		VALUES ('dev-1','ALPHA-PC','windows',?)`, caps)

	h := &Handler{
		repo:  &Repository{db: d},
		hub:   hub,
		audit: nullAuditor{},
		devices: deviceLookup{known: map[string]devicemgmt.Device{
			"dev-1": {ID: "dev-1", Hostname: "ALPHA-PC", OSName: "windows"},
		}},
	}

	r := chi.NewRouter()
	r.Post("/api/devices/{id}/software/uninstall", h.uninstallDeviceSoftware)
	return r, d
}

// postUninstall issues the request as a logged-in operator; the user id has to
// be in the context because the audit entry reads it.
func postUninstall(t *testing.T, r *chi.Mux, deviceID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/devices/"+deviceID+"/software/uninstall", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "u-1"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func commandRows(t *testing.T, d *sqlx.DB) int {
	t.Helper()
	var n int
	if err := d.Get(&n, `SELECT COUNT(*) FROM agent_commands`); err != nil {
		t.Fatal(err)
	}
	return n
}

// The happy path, asserted end to end: 201, exactly one command row, and the
// name the operator clicked actually on the wire.
func TestUninstallByNameDispatchesToAnOnlineCapableAgent(t *testing.T) {
	hub := &uninstallHub{online: true}
	r, d := uninstallFixture(t, `["software.uninstall.by_name"]`, hub)

	rec := postUninstall(t, r, "dev-1", `{"software_name":"PostgreSQL 16"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: body %s", rec.Code, rec.Body.String())
	}
	if n := commandRows(t, d); n != 1 {
		t.Fatalf("agent_commands has %d rows, want 1", n)
	}
	if len(hub.sent) != 1 {
		t.Fatalf("hub received %d sends, want 1", len(hub.sent))
	}

	var env struct {
		ID      string            `json:"id"`
		Command string            `json:"command"`
		Payload map[string]string `json:"payload"`
	}
	if err := json.Unmarshal(hub.sent[0], &env); err != nil {
		t.Fatal(err)
	}
	if env.Command != "software.uninstall.by_name" {
		t.Errorf("command = %q, want software.uninstall.by_name", env.Command)
	}
	if env.Payload["software_name"] != "PostgreSQL 16" {
		t.Errorf("payload = %v, want the software name the operator clicked", env.Payload)
	}

	// The body must not carry uninstall arguments. There is no field for the
	// operator to guess one into: that guess is what opens a window on the
	// endpoint. The agent resolves switches from the registry or refuses.
	if strings.Contains(strings.ToLower(string(hub.sent[0])), "uninstall_args") {
		t.Errorf("command payload carries uninstall_args: %s", hub.sent[0])
	}
}

// An offline endpoint is a refusal, not a queue.
//
// The ping route above answers "queued" with 200 when a device is offline, and
// for a ping that is harmless. Here it is not: the operator is looking at a
// program that is still on the machine, presses Uninstall, and is told it went
// through. They close the tab and the program is still there. 409 with no row
// created is the only answer that cannot be misread.
func TestUninstallByNameRefusesAnOfflineEndpoint(t *testing.T) {
	hub := &uninstallHub{online: false}
	r, d := uninstallFixture(t, `["software.uninstall.by_name"]`, hub)

	rec := postUninstall(t, r, "dev-1", `{"software_name":"PostgreSQL 16"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409: an operator must not be told an uninstall was queued "+
			"to an endpoint that will never read it (body: %s)", rec.Code, rec.Body.String())
	}
	if n := commandRows(t, d); n != 0 {
		t.Errorf("agent_commands has %d rows, want 0: nothing should be queued for an offline agent", n)
	}
	if len(hub.sent) != 0 {
		t.Errorf("hub received %d sends, want 0", len(hub.sent))
	}
}

// An agent predating this command drops it silently. Its result would land in
// agent_commands and not in deployment_tasks, so the orphan sweep -- which only
// reaps offline devices -- would never touch the row: it would sit at 'sent'
// forever on a perfectly healthy endpoint.
func TestUninstallByNameRefusesAnAgentThatCannotHandleIt(t *testing.T) {
	hub := &uninstallHub{online: true}
	r, d := uninstallFixture(t, `["software.install","software.uninstall"]`, hub)

	rec := postUninstall(t, r, "dev-1", `{"software_name":"PostgreSQL 16"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "ALPHA-PC") && !strings.Contains(body, "dev-1") {
		t.Errorf("the refusal should name the endpoint so the operator knows which agent to update: %s", body)
	}
	if n := commandRows(t, d); n != 0 {
		t.Errorf("agent_commands has %d rows, want 0", n)
	}
	if len(hub.sent) != 0 {
		t.Errorf("hub received %d sends, want 0", len(hub.sent))
	}
}

// A NULL capability list is treated as lacking everything, which is the
// conservative direction: capabilities are NULL until an agent first reconnects
// with a build that sends them.
func TestUninstallByNameRefusesADeviceWithNoStoredCapabilities(t *testing.T) {
	hub := &uninstallHub{online: true}
	r, d := uninstallFixture(t, ``, hub)

	rec := postUninstall(t, r, "dev-1", `{"software_name":"PostgreSQL 16"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if n := commandRows(t, d); n != 0 {
		t.Errorf("agent_commands has %d rows, want 0", n)
	}
}

// An empty name is the shape a UI bug produces. It must never become a command,
// because "matches everything" is a very bad thing to hand to code that runs
// uninstallers.
func TestUninstallByNameRefusesAnEmptyName(t *testing.T) {
	hub := &uninstallHub{online: true}
	r, d := uninstallFixture(t, `["software.uninstall.by_name"]`, hub)

	for _, body := range []string{`{"software_name":""}`, `{"software_name":"   "}`, `{}`} {
		rec := postUninstall(t, r, "dev-1", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, rec.Code)
		}
	}
	if n := commandRows(t, d); n != 0 {
		t.Errorf("agent_commands has %d rows, want 0", n)
	}
}

func TestUninstallByNameRefusesAnUnknownDevice(t *testing.T) {
	hub := &uninstallHub{online: true}
	r, d := uninstallFixture(t, `["software.uninstall.by_name"]`, hub)

	rec := postUninstall(t, r, "dev-nope", `{"software_name":"PostgreSQL 16"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if n := commandRows(t, d); n != 0 {
		t.Errorf("agent_commands has %d rows, want 0", n)
	}
}

// SendTo returning false means the agent's queue is full and nothing was
// delivered. The row is left in place deliberately: it says 'sent' while
// nothing ran, which is honest, and the orphan sweep reaps it if the agent goes
// offline. Deleting it would erase the only trace of the attempt. The operator
// still has to be told this attempt failed -- that is the part being asserted.
func TestUninstallByNameTellsTheOperatorWhenTheSendIsDropped(t *testing.T) {
	hub := &uninstallHub{online: true, dropSend: true}
	r, d := uninstallFixture(t, `["software.uninstall.by_name"]`, hub)

	rec := postUninstall(t, r, "dev-1", `{"software_name":"PostgreSQL 16"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409: the command was not delivered, so the operator must not "+
			"be told it was sent (body: %s)", rec.Code, rec.Body.String())
	}
	if n := commandRows(t, d); n != 1 {
		t.Errorf("agent_commands has %d rows, want 1: the row records the attempt and stays 'sent'", n)
	}

	var status string
	if err := d.Get(&status, `SELECT status FROM agent_commands`); err != nil {
		t.Fatal(err)
	}
	if status == "done" {
		t.Error("a command whose send was dropped is recorded as done; it would claim a removal " +
			"that never ran")
	}
}

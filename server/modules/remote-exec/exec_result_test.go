package remoteexec

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// agentValidator answers for the two devices the fixture enrols, keyed by the
// hash of the secret the handler is about to compute. FindBySecretHash and
// GetByID are both consulted by the handlers under test, so both are here.
type agentValidator struct{}

var fixtureDevices = map[string]string{
	"attacker-secret": "device-attacker",
	"victim-secret":   "device-victim",
}

func (agentValidator) FindBySecretHash(_ context.Context, hash string) (devicemgmt.Device, error) {
	for secret, id := range fixtureDevices {
		if devicemgmt.HashToken(secret) == hash {
			return devicemgmt.Device{ID: id}, nil
		}
	}
	return devicemgmt.Device{}, errors.New("no such device")
}

func (agentValidator) GetByID(_ context.Context, id string) (devicemgmt.Device, error) {
	for _, known := range fixtureDevices {
		if known == id {
			return devicemgmt.Device{ID: id}, nil
		}
	}
	return devicemgmt.Device{}, errors.New("no such device")
}

// execFixture is one database holding a single in-flight execution belonging to
// device-victim. A file database with the production DSN, for the reason
// terminal_dispatch_test.go gives: ":memory:" gives every pooled connection its
// own database, and db.DSNForPath is what turns the resulting write contention
// into a wait rather than SQLITE_BUSY.
func execFixture(t *testing.T, online, accepts bool) (*chi.Mux, *sqlx.DB, *recordingAudit) {
	t.Helper()

	database, err := sqlx.Open("sqlite", db.DSNForPath(filepath.Join(t.TempDir(), "exec.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	database.MustExec(`CREATE TABLE remote_executions (
		id TEXT PRIMARY KEY,
		device_id TEXT NOT NULL,
		operator_id TEXT NOT NULL,
		shell_type TEXT NOT NULL,
		command_text TEXT NOT NULL,
		status TEXT NOT NULL,
		exit_code INTEGER,
		output TEXT,
		error_message TEXT,
		started_at DATETIME NOT NULL,
		completed_at DATETIME
	);`)
	// operator_name and hostname are COALESCEd out of these two tables by every
	// read query in the repository, so a fixture without them fails with
	// "no such table" rather than returning a body.
	database.MustExec(`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT);`)
	database.MustExec(`CREATE TABLE devices (id TEXT PRIMARY KEY, hostname TEXT);`)
	database.MustExec(`INSERT INTO remote_executions
		(id, device_id, operator_id, shell_type, command_text, status, started_at)
		VALUES ('exec-victim','device-victim','u-1','powershell','hostname','running',?)`,
		time.Now().UTC())

	auditor := &recordingAudit{}
	h := NewHandler(
		NewRepository(database),
		&wedgedHub{online: online, accepts: accepts},
		NewTerminalRelay(),
		auditor,
		auth.NewJWTService("exec-result-fixture-secret", time.Hour, 24*time.Hour),
		func(n http.Handler) http.Handler { return n },
		agentValidator{},
		func(*http.Request) bool { return true },
	)

	r := chi.NewRouter()
	r.Post("/api/agent/executions/{id}/result", h.reportExecutionResult)
	r.Post("/api/devices/{id}/exec", h.executeCommand)
	r.Get("/api/devices/{id}/executions", h.listExecutions)
	r.Get("/api/devices/{id}/executions/{execId}", h.getExecution)
	return r, database, auditor
}

func resultReportAs(t *testing.T, r *chi.Mux, deviceID, secret, execID, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/agent/executions/"+execID+"/result", strings.NewReader(body))
	req.Header.Set("X-Device-Id", deviceID)
	req.Header.Set("X-Device-Secret", secret)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}

func execRow(t *testing.T, database *sqlx.DB, id string) (status, output string) {
	t.Helper()
	if err := database.Get(&status, `SELECT status FROM remote_executions WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	var raw *string
	if err := database.Get(&raw, `SELECT output FROM remote_executions WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		output = *raw
	}
	return status, output
}

// TestAnAgentCannotCompleteAnotherDevicesExecution is the regression test for a
// cross-device write.
//
// reportExecutionResult authenticated the agent, then took the execution id
// straight from the URL and handed it to an UPDATE that matched on `id` alone.
// Any enrolled agent could therefore name any execution in the fleet, mark it
// completed, and supply its own exit code and output.
//
// This is the same defect that was already found and fixed on the deployment
// task table (report_progress_test.go in the software-deployment module) and was
// left in place here, so the two endpoints that report command results behaved
// in opposite ways: one scoped the write to the reporting device, the other did
// not. An attacker holding one enrolled agent rewrites the record of what a
// technician ran on a machine they were never authorised to reach, and can make a
// command that timed out or was killed read as a clean success.
func TestAnAgentCannotCompleteAnotherDevicesExecution(t *testing.T) {
	r, database, _ := execFixture(t, false, false)

	code := resultReportAs(t, r, "device-attacker", "attacker-secret", "exec-victim",
		`{"status":"completed","exit_code":0,"output":"pwned"}`)

	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: the write should not have been applied", code)
	}
	status, output := execRow(t, database, "exec-victim")
	if status != "running" {
		t.Errorf("victim execution status = %q, want \"running\": another device's "+
			"execution was rewritten", status)
	}
	if output == "pwned" {
		t.Error("victim execution output was overwritten by an agent that does not own it")
	}
}

// TestAnAgentCanStillReportItsOwnExecution is the other half: the scoping must
// not break the ordinary path, or every command would sit at 'running' forever.
func TestAnAgentCanStillReportItsOwnExecution(t *testing.T) {
	r, database, auditor := execFixture(t, false, false)

	code := resultReportAs(t, r, "device-victim", "victim-secret", "exec-victim",
		`{"status":"completed","exit_code":0,"output":"WEDGE-PC"}`)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: the owner must still be able to report", code)
	}
	status, output := execRow(t, database, "exec-victim")
	if status != "completed" {
		t.Errorf("status = %q, want \"completed\"", status)
	}
	if output != "WEDGE-PC" {
		t.Errorf("output = %q, want \"WEDGE-PC\"", output)
	}

	var completed *time.Time
	if err := database.Get(&completed,
		`SELECT completed_at FROM remote_executions WHERE id = 'exec-victim'`); err != nil {
		t.Fatal(err)
	}
	if completed == nil {
		t.Error("completed_at is NULL on a terminal execution; the console sorts and " +
			"filters on it")
	}

	var recorded bool
	for _, a := range auditor.actions {
		if a == "remote_exec.result" {
			recorded = true
		}
	}
	if !recorded {
		t.Errorf("audit actions = %v, want a remote_exec.result entry: a command "+
			"result with no audit trail is a result nobody can account for", auditor.actions)
	}
}

// TestAnUnknownExecutionIsIndistinguishableFromAnotherDevicesExecution keeps the
// refusal from becoming an oracle. If a missing id answered differently from
// someone else's, an agent could enumerate which executions exist.
func TestAnUnknownExecutionIsIndistinguishableFromAnotherDevicesExecution(t *testing.T) {
	r, _, _ := execFixture(t, false, false)

	unknown := resultReportAs(t, r, "device-victim", "victim-secret", "no-such-exec",
		`{"status":"completed"}`)
	otherDevice := resultReportAs(t, r, "device-attacker", "attacker-secret", "exec-victim",
		`{"status":"completed"}`)

	if unknown != otherDevice {
		t.Errorf("unknown execution answered %d but another device's execution answered "+
			"%d; the difference tells an agent which execution ids exist", unknown, otherDevice)
	}
}

// TestAnUnauthenticatedAgentCannotReportAResult confirms the scoping did not
// replace the existing credential check with a weaker one, and that a valid
// secret paired with another device's id is still refused.
func TestAnUnauthenticatedAgentCannotReportAResult(t *testing.T) {
	r, database, _ := execFixture(t, false, false)

	cases := []struct {
		name   string
		id     string
		secret string
	}{
		{"wrong secret", "device-victim", "not-the-secret"},
		{"valid secret on another device's id", "device-attacker", "victim-secret"},
		{"empty credentials", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code := resultReportAs(t, r, tc.id, tc.secret, "exec-victim",
				`{"status":"completed","output":"pwned"}`); code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", code)
			}
			status, _ := execRow(t, database, "exec-victim")
			if status != "running" {
				t.Errorf("status = %q, want \"running\"", status)
			}
		})
	}
}

// A malformed body must be refused before any write: decoding it is the only
// thing standing between a truncated report and a blanked output log.
func TestAMalformedResultBodyIsRefused(t *testing.T) {
	r, database, _ := execFixture(t, false, false)

	code := resultReportAs(t, r, "device-victim", "victim-secret", "exec-victim", `{"status":`)

	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
	status, _ := execRow(t, database, "exec-victim")
	if status != "running" {
		t.Errorf("status = %q, want \"running\"", status)
	}
}

// TestTheEmptyExecutionListReadsAsAnArray pins the read endpoints, because the
// console iterates the list body without a null check: Go encodes a nil slice as
// `null`, and `for (const x of null)` throws in the browser.
func TestTheEmptyExecutionListReadsAsAnArray(t *testing.T) {
	r, _, _ := execFixture(t, false, false)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/devices/no-such-device/executions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("empty list body = %s, want []: the console iterates this without a "+
			"null check", got)
	}
}

func TestExecutionReadsBackAndAMissingOneIs404(t *testing.T) {
	r, _, _ := execFixture(t, false, false)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/devices/device-victim/executions/exec-victim", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200", rec.Code)
	}
	var exec RemoteExecution
	if err := json.Unmarshal(rec.Body.Bytes(), &exec); err != nil {
		t.Fatal(err)
	}
	if exec.ID != "exec-victim" || exec.Hostname != "unknown" {
		t.Errorf("execution = %+v: want exec-victim, with hostname falling back to "+
			"\"unknown\" when there is no devices table to join", exec)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/devices/device-victim/executions/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown execution status = %d, want 404", rec.Code)
	}
}

// TestDispatchRefusesAnUnknownDeviceAndAnOfflineOne: executeCommand runs a shell
// on someone else's machine, so the device has to exist and it has to be
// connected. Otherwise the operator is told a command was dispatched to a host
// that never received it.
func TestDispatchRefusesAnUnknownDeviceAndAnOfflineOne(t *testing.T) {
	cases := []struct {
		name   string
		device string
		online bool
		want   int
	}{
		{"unknown device", "no-such-device", true, http.StatusNotFound},
		{"device offline", "device-victim", false, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, database, _ := execFixture(t, tc.online, tc.online)

			req := httptest.NewRequest(http.MethodPost, "/api/devices/"+tc.device+"/exec",
				strings.NewReader(`{"command":"hostname"}`))
			req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "u-1"))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d\nbody: %s", rec.Code, tc.want, rec.Body.String())
			}
			var n int
			if err := database.Get(&n, `SELECT COUNT(*) FROM remote_executions`); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Errorf("execution rows = %d, want 1: a refused dispatch must not leave a row", n)
			}
		})
	}
}

// TestDispatchRefusesWhatTheAgentWouldOnlyRejectAtTheEndpoint: an empty command,
// a body that is not JSON, and a shell from another platform all have to be
// refused here. Each would otherwise reach the machine and fail there, which is
// the expensive place to find out and shows the operator an unexplained failure.
func TestDispatchRefusesWhatTheAgentWouldOnlyRejectAtTheEndpoint(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"not json", `command=hostname`, http.StatusBadRequest},
		{"empty command", `{"command":""}`, http.StatusBadRequest},
		{"shell from another platform", `{"command":"ls","shell":"zsh"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _ := execFixture(t, true, true)

			req := httptest.NewRequest(http.MethodPost, "/api/devices/device-victim/exec",
				strings.NewReader(tc.body))
			req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "u-1"))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d\nbody: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// TestADispatchTheAgentSocketDropsIsRecordedAsFailed covers the one branch that
// looks like success to the hub: the device is online, so the offline gate
// passes, but its 64-slot send queue is full and SendTo comes back false. The
// operator must be told the dispatch failed, and the row must not be left at
// 'running' for a command that was never delivered.
func TestADispatchTheAgentSocketDropsIsRecordedAsFailed(t *testing.T) {
	// Online true, SendTo false: the only combination that reaches the send.
	// The offline gate passes, then the device's 64-slot queue is full and the
	// command never leaves the server.
	r, database, _ := execFixture(t, true, false)

	req := httptest.NewRequest(http.MethodPost, "/api/devices/device-victim/exec",
		strings.NewReader(`{"command":"hostname"}`))
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "u-1"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: the operator was told a command was "+
			"dispatched to a device that never received it\nbody: %s",
			rec.Code, rec.Body.String())
	}

	// The row executeCommand wrote before the send has to be closed out. Left at
	// 'running' it is indistinguishable from a command that is genuinely still
	// going, and nothing sweeps it.
	var status, msg string
	if err := database.Get(&status,
		`SELECT status FROM remote_executions WHERE status <> 'running'`); err != nil {
		t.Fatal(err)
	}
	if status != ExecStatusFailed {
		t.Errorf("status = %q, want %q", status, ExecStatusFailed)
	}
	if err := database.Get(&msg,
		`SELECT error_message FROM remote_executions WHERE status = ?`, ExecStatusFailed); err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Error("no error message on the failed dispatch; the operator sees a red row " +
			"with no explanation")
	}
}

// TestADispatchTheAgentAcceptsStillCreatesARunningExecution is the other half of
// the branch above: a hub that takes the command has to leave a live row, or the
// fix would have cost every real command its history entry.
func TestADispatchTheAgentAcceptsStillCreatesARunningExecution(t *testing.T) {
	r, _, auditor := execFixture(t, true, true)

	req := httptest.NewRequest(http.MethodPost, "/api/devices/device-victim/exec",
		strings.NewReader(`{"command":"hostname","shell":"cmd"}`))
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "u-1"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Status    string          `json:"status"`
		Execution RemoteExecution `json:"execution"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "dispatched" {
		t.Errorf("status = %q, want \"dispatched\"", body.Status)
	}
	if body.Execution.ShellType != ShellCMD {
		t.Errorf("shell_type = %q, want %q", body.Execution.ShellType, ShellCMD)
	}
	if body.Execution.OperatorID != "u-1" {
		t.Errorf("operator_id = %q, want \"u-1\": the execution has to name who ran it",
			body.Execution.OperatorID)
	}

	var recorded bool
	for _, a := range auditor.actions {
		if a == "remote_exec.run" {
			recorded = true
		}
	}
	if !recorded {
		t.Errorf("audit actions = %v, want a remote_exec.run entry: running a shell "+
			"on another machine with no audit trail is the worst thing this module does",
			auditor.actions)
	}
}

// TestAnEmptyShellDefaultsToPowerShellAndTheTimeoutIsClamped pins the two
// normalisations. An unclamped timeout is a request the agent will honour as
// written, so an operator asking for 999999 gets a command that runs for a very
// long time on a machine they are remotely driving.
func TestAnEmptyShellDefaultsToPowerShellAndTheTimeoutIsClamped(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantShell  string
		wantTimout int
	}{
		{"defaults", `{"command":"hostname"}`, ShellPowerShell, 60},
		{"clamped high", `{"command":"hostname","timeout_sec":999999}`, ShellPowerShell, 300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := &capturingHub{}
			h := NewHandler(NewRepository(execSchema(t)), hub, NewTerminalRelay(),
				&recordingAudit{},
				auth.NewJWTService("exec-normalisation-secret", time.Hour, 24*time.Hour),
				func(n http.Handler) http.Handler { return n },
				agentValidator{}, func(*http.Request) bool { return true })

			req := httptest.NewRequest(http.MethodPost, "/api/devices/device-victim/exec",
				strings.NewReader(tc.body))
			req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "u-1"))
			rec := httptest.NewRecorder()
			h.executeCommand(rec, withRouteParam(req, "id", "device-victim"))

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
			}
			if hub.payload == nil {
				t.Fatal("nothing was sent to the agent")
			}
			var env struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Payload struct {
					Shell      string `json:"shell"`
					TimeoutSec int    `json:"timeout_sec"`
				} `json:"payload"`
			}
			if err := json.Unmarshal(hub.payload, &env); err != nil {
				t.Fatal(err)
			}
			if env.Command != "exec.run" {
				t.Errorf("command = %q, want \"exec.run\"", env.Command)
			}
			// The clamp is what the agent will actually honour, so it has to be
			// correct on the wire, not only in the response body.
			if env.Payload.TimeoutSec != tc.wantTimout {
				t.Errorf("timeout_sec = %d, want %d", env.Payload.TimeoutSec, tc.wantTimout)
			}
			if env.Payload.Shell != tc.wantShell {
				t.Errorf("shell = %q, want %q", env.Payload.Shell, tc.wantShell)
			}
		})
	}
}

// capturingHub keeps the bytes it was handed so a test can assert on the wire
// format rather than only on the HTTP response.
type capturingHub struct{ payload []byte }

func (c *capturingHub) Online(string) bool { return true }

func (c *capturingHub) SendTo(_ string, msg []byte) bool {
	c.payload = msg
	return true
}

func execSchema(t *testing.T) *sqlx.DB {
	t.Helper()
	database, err := sqlx.Open("sqlite", db.DSNForPath(filepath.Join(t.TempDir(), "exec.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	database.MustExec(`CREATE TABLE remote_executions (
		id TEXT PRIMARY KEY, device_id TEXT NOT NULL, operator_id TEXT NOT NULL,
		shell_type TEXT NOT NULL, command_text TEXT NOT NULL, status TEXT NOT NULL,
		exit_code INTEGER, output TEXT, error_message TEXT,
		started_at DATETIME NOT NULL, completed_at DATETIME);`)
	return database
}

func withRouteParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

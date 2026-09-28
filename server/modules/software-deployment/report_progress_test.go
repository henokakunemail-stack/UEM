package softwaredeployment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"

	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

type silentHub struct{}

func (silentHub) Online(string) bool         { return false }
func (silentHub) SendTo(string, []byte) bool { return true }

type silentAuditor struct{}

func (silentAuditor) Log(context.Context, string, string, string, string, map[string]string) error {
	return nil
}

// Two enrolled devices, keyed by the hash of the secret the real repository
// would store, because AuthenticateAgent hashes the presented secret itself.
type lookup struct{ devices map[string]devicemgmt.Device }

func (l lookup) FindBySecretHash(_ context.Context, hash string) (devicemgmt.Device, error) {
	d, ok := l.devices[hash]
	if !ok {
		return devicemgmt.Device{}, context.Canceled
	}
	return d, nil
}

func newProgressFixture(t *testing.T) (*Handler, *chi.Mux, *sqlx.DB) {
	t.Helper()
	d, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	d.MustExec(`CREATE TABLE deployment_tasks (id TEXT PRIMARY KEY, device_id TEXT,
		deployment_id TEXT, status TEXT, exit_code INTEGER, output_log TEXT,
		error_message TEXT, updated_at TEXT, completed_at TEXT);`)
	d.MustExec(`CREATE TABLE software_deployments (id TEXT PRIMARY KEY, status TEXT, completed_at TEXT);`)
	d.MustExec(`INSERT INTO deployment_tasks (id, device_id, deployment_id, status)
		VALUES ('task-victim','device-victim','dep1','installing')`)

	h := NewHandler(&Repository{db: d}, silentHub{}, silentAuditor{}, t.TempDir(),
		func(n http.Handler) http.Handler { return n },
		lookup{devices: map[string]devicemgmt.Device{
			devicemgmt.HashToken("attacker-secret"): {ID: "device-attacker"},
			devicemgmt.HashToken("victim-secret"):   {ID: "device-victim"},
		}})

	r := chi.NewRouter()
	r.Post("/api/agent/tasks/{id}/progress", h.reportProgress)
	return h, r, d
}

func reportAs(t *testing.T, r *chi.Mux, deviceID, secret, taskID, body string) int {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/agent/tasks/"+taskID+"/progress", strings.NewReader(body))
	req.Header.Set("X-Device-Id", deviceID)
	req.Header.Set("X-Device-Secret", secret)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}

func taskRow(t *testing.T, d *sqlx.DB, id string) (status, output string) {
	t.Helper()
	var exitCode *int
	if err := d.Get(&status, `SELECT status FROM deployment_tasks WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := d.Get(&output, `SELECT COALESCE(output_log, '') FROM deployment_tasks WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	_ = exitCode
	return status, output
}

// TestAnAgentCannotReportProgressForAnotherDevicesTask is the regression test
// for a cross-device write.
//
// reportProgress authenticated the agent and then took the task id straight from
// the URL, and the UPDATE matched on `id` alone. Any enrolled agent could
// therefore name any task in the fleet and mark it success, supply its own exit
// code and output log, and have the parent deployment recomputed from that. One
// agent reporting five successes for five other devices' tasks turns a failed
// rollout into a green Completed badge, and the real result of an install still
// running is overwritten rather than appended to.
func TestAnAgentCannotReportProgressForAnotherDevicesTask(t *testing.T) {
	_, r, d := newProgressFixture(t)

	code := reportAs(t, r, "device-attacker", "attacker-secret", "task-victim",
		`{"status":"success","exit_code":0,"output_log":"pwned"}`)

	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: the write should not have been applied", code)
	}
	status, output := taskRow(t, d, "task-victim")
	if status != "installing" {
		t.Errorf("victim task status = %q, want \"installing\": another device's task was written", status)
	}
	if output == "pwned" {
		t.Error("victim task output_log was overwritten by an agent that does not own it")
	}
}

// TestAnAgentCanStillReportItsOwnTask is the other half: the scoping must not
// break the ordinary path, or installs would silently never report.
func TestAnAgentCanStillReportItsOwnTask(t *testing.T) {
	_, r, d := newProgressFixture(t)

	code := reportAs(t, r, "device-victim", "victim-secret", "task-victim",
		`{"status":"success","exit_code":0,"output_log":"installed"}`)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: the owner must still be able to report", code)
	}
	status, output := taskRow(t, d, "task-victim")
	if status != "success" {
		t.Errorf("status = %q, want \"success\"", status)
	}
	if output != "installed" {
		t.Errorf("output_log = %q, want \"installed\"", output)
	}
}

// TestProgressOnAnUnknownTaskIsIndistinguishableFromAnotherDevicesTask keeps
// the refusal from becoming an oracle. If a missing task answered differently
// from someone else's, an agent could enumerate the fleet's task ids.
func TestProgressOnAnUnknownTaskIsIndistinguishableFromAnotherDevicesTask(t *testing.T) {
	_, r, _ := newProgressFixture(t)

	unknown := reportAs(t, r, "device-victim", "victim-secret", "no-such-task",
		`{"status":"success"}`)
	otherDevice := reportAs(t, r, "device-attacker", "attacker-secret", "task-victim",
		`{"status":"success"}`)

	if unknown != otherDevice {
		t.Errorf("unknown task answered %d but another device's task answered %d; "+
			"the difference tells an agent which task ids exist", unknown, otherDevice)
	}
}

// TestAnUnauthenticatedAgentCannotReportProgress confirms the scoping did not
// replace the existing check with a weaker one.
func TestAnUnauthenticatedAgentCannotReportProgress(t *testing.T) {
	_, r, d := newProgressFixture(t)

	code := reportAs(t, r, "device-victim", "wrong-secret", "task-victim",
		`{"status":"success","output_log":"pwned"}`)

	if code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", code)
	}
	status, _ := taskRow(t, d, "task-victim")
	if status != "installing" {
		t.Errorf("status = %q, want \"installing\"", status)
	}
}

package taskscheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

type stubHub struct{}

func (stubHub) Online(string) bool         { return true }
func (stubHub) SendTo(string, []byte) bool { return true }

type nopAuditor struct{}

func (nopAuditor) Log(context.Context, string, string, string, string, map[string]string) error {
	return nil
}

// deviceLookup resolves a presented secret hash to a device, the same contract
// devicemgmt.AuthenticateAgent expects.
type deviceLookup map[string]devicemgmt.Device

func (d deviceLookup) FindBySecretHash(_ context.Context, hash string) (devicemgmt.Device, error) {
	dev, ok := d[hash]
	if !ok {
		return devicemgmt.Device{}, devicemgmt.ErrNotFound
	}
	return dev, nil
}

type runFixture struct {
	repo    *Repository
	db      *sqlx.DB
	runID   string
	secrets deviceLookup
}

func newRunFixture(t *testing.T, deviceIDs []string) *runFixture {
	t.Helper()

	database, err := db.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC()
	secrets := deviceLookup{}
	for _, id := range deviceIDs {
		if _, err := database.Exec(`
			INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
				device_secret_hash, enrolled_at, created_at, updated_at)
			VALUES (?, ?, 'windows', '11', '1.0.0', 'online', 'x', ?, ?, ?)`,
			id, id, now, now, now); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		secrets[devicemgmt.HashToken("secret-"+id)] = devicemgmt.Device{ID: id}
	}
	if _, err := database.Exec(`
		INSERT INTO script_templates (id, name, script_type, script_content, sha256_hash,
			timeout_seconds, created_by, created_at, updated_at)
		VALUES ('script-1','s','powershell','echo hi','abc',300,'u1',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO task_schedules (id, name, script_id, target_type, target_id,
			schedule_type, schedule_expr, is_enabled, created_by, created_at, updated_at)
		VALUES ('sched-1','s','script-1','all','','interval','60',1,'u1',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(database)
	run, err := NewScheduler(repo, stubHub{}).TriggerSchedule(context.Background(), "sched-1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	return &runFixture{repo: repo, db: database, runID: run.ID, secrets: secrets}
}

func (f *runFixture) handler() *Handler {
	return &Handler{repo: f.repo, audit: nopAuditor{}, devices: f.secrets}
}

// report posts a result as the given device, exactly as the agent would.
func (f *runFixture) report(t *testing.T, deviceID, deviceRunID, status string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"status":"` + status + `","exit_code":0,"output_log":"done"}`
	req := httptest.NewRequest(http.MethodPost,
		"/api/agent/schedules/tasks/"+deviceRunID+"/result", strings.NewReader(body))
	req.Header.Set("X-Device-Id", deviceID)
	req.Header.Set("X-Device-Secret", "secret-"+deviceID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", deviceRunID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	f.handler().reportTaskResult(rec, req)
	return rec
}

func (f *runFixture) deviceRunIDs(t *testing.T) []string {
	t.Helper()
	var ids []string
	if err := f.db.Select(&ids,
		`SELECT id FROM scheduled_task_device_runs WHERE run_id = ? ORDER BY id`, f.runID); err != nil {
		t.Fatal(err)
	}
	return ids
}

// deviceRunFor pairs each device with the device run it was given.
func (f *runFixture) deviceRunFor(t *testing.T, deviceID string) string {
	t.Helper()
	var id string
	if err := f.db.Get(&id,
		`SELECT id FROM scheduled_task_device_runs WHERE run_id = ? AND device_id = ?`,
		f.runID, deviceID); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *runFixture) parent(t *testing.T) (status string, completedAt *time.Time) {
	t.Helper()
	if err := f.db.Get(&status, `SELECT status FROM scheduled_task_runs WHERE id = ?`, f.runID); err != nil {
		t.Fatal(err)
	}
	_ = f.db.Get(&completedAt, `SELECT completed_at FROM scheduled_task_runs WHERE id = ?`, f.runID)
	return status, completedAt
}

// TestAFullySuccessfulRunReachesATerminalStatus is the regression test for a row
// nothing ever wrote again.
//
// CreateRun inserts the parent as 'running'. CompleteRun was the only thing that
// moved it, and TriggerSchedule called that on exactly one branch -- when the
// target resolved to zero devices. On every other branch the parent was written
// once and never touched again: the agent result handler updated the device run
// and returned, and the background scheduler only triggers new runs. So a script
// dispatched to five endpoints that all reported success left the operator with
// a run stuck in 'running' with a NULL completed_at, next to five green device
// rows, for the life of the installation.
//
// Nothing in the UI distinguishes that from a run still executing, and the
// history view shows that row forever.
func TestAFullySuccessfulRunReachesATerminalStatus(t *testing.T) {
	devices := []string{"d1", "d2", "d3", "d4", "d5"}
	f := newRunFixture(t, devices)

	if got := len(f.deviceRunIDs(t)); got != 5 {
		t.Fatalf("%d device runs created, want 5", got)
	}

	// One result in, the parent must still be running -- that is the honest
	// state and the row's own default.
	if rec := f.report(t, "d1", f.deviceRunFor(t, "d1"), "success"); rec.Code != http.StatusOK {
		t.Fatalf("first result -> %d %s", rec.Code, rec.Body.String())
	}
	if status, _ := f.parent(t); status != "running" {
		t.Errorf("parent status = %q after 1 of 5 results, want \"running\"", status)
	}

	for _, id := range devices[1:] {
		if rec := f.report(t, id, f.deviceRunFor(t, id), "success"); rec.Code != http.StatusOK {
			t.Fatalf("result for %s -> %d %s", id, rec.Code, rec.Body.String())
		}
	}

	status, completedAt := f.parent(t)
	if status != "completed" {
		t.Errorf("parent status = %q after all 5 devices reported success, want "+
			"\"completed\": every endpoint finished and the run still claims to be running", status)
	}
	if completedAt == nil {
		t.Error("completed_at is NULL on a finished run; the console has no end time to show")
	}
}

// TestAPartiallyReportedRunStaysRunning is the other half: the rollup must not
// fire early. Completing a run while devices are still executing is the same
// lie pointed the other way.
func TestAPartiallyReportedRunStaysRunning(t *testing.T) {
	f := newRunFixture(t, []string{"d1", "d2", "d3"})

	for _, id := range []string{"d1", "d2"} {
		if rec := f.report(t, id, f.deviceRunFor(t, id), "success"); rec.Code != http.StatusOK {
			t.Fatalf("result for %s -> %d", id, rec.Code)
		}
	}

	status, completedAt := f.parent(t)
	if status != "running" {
		t.Errorf("parent status = %q with 1 of 3 devices outstanding, want \"running\"", status)
	}
	if completedAt != nil {
		t.Errorf("completed_at = %v with a device still running, want NULL", *completedAt)
	}
}

// TestAFullyFailedRunIsFailedNotCompleted: an all-failed rollout reaching
// "completed" renders as a green success badge, which is the exact outcome the
// software-deployment rollup was already corrected for.
func TestAFullyFailedRunIsFailedNotCompleted(t *testing.T) {
	f := newRunFixture(t, []string{"d1", "d2"})

	for _, id := range []string{"d1", "d2"} {
		if rec := f.report(t, id, f.deviceRunFor(t, id), "failed"); rec.Code != http.StatusOK {
			t.Fatalf("result for %s -> %d", id, rec.Code)
		}
	}
	if status, _ := f.parent(t); status != "failed" {
		t.Errorf("parent status = %q after every device failed, want \"failed\": a run "+
			"that succeeded nowhere must not read as a success", status)
	}
}

// TestAPartlySuccessfulRunIsCompleted: the rollup asks "did everything finish",
// not "did everything succeed". One of two endpoints failing still completed.
func TestAPartlySuccessfulRunIsCompleted(t *testing.T) {
	f := newRunFixture(t, []string{"d1", "d2"})

	if rec := f.report(t, "d1", f.deviceRunFor(t, "d1"), "success"); rec.Code != http.StatusOK {
		t.Fatalf("d1 -> %d", rec.Code)
	}
	if rec := f.report(t, "d2", f.deviceRunFor(t, "d2"), "failed"); rec.Code != http.StatusOK {
		t.Fatalf("d2 -> %d", rec.Code)
	}
	if status, _ := f.parent(t); status != "completed" {
		t.Errorf("parent status = %q, want \"completed\": every endpoint reported", status)
	}
}

// TestAnAgentCannotReportAnotherDevicesResult is the regression test for a
// cross-device write.
//
// reportTaskResult authenticated the agent and then took the run id straight
// from the URL, and UpdateDeviceRunResult matched on it alone. The handler's
// own comment described the hazard it was guarding against -- "any client that
// could reach the port could write results for arbitrary scheduled tasks" --
// and authentication does close the anonymous case, but an agent already
// enrolled in the fleet reaches the same outcome. It could write status, exit
// code and output log for any other endpoint's run, and with the rollup in
// place the parent run would be recomputed from that forged result.
//
// An agent is a lower-trust client than an operator: it runs unattended on a
// machine an operator does not physically control, and its credentials are
// distributed to the whole fleet rather than issued per person.
func TestAnAgentCannotReportAnotherDevicesResult(t *testing.T) {
	f := newRunFixture(t, []string{"d1", "d2"})

	victimRun := f.deviceRunFor(t, "d2")
	rec := f.report(t, "d1", victimRun, "success")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: another device's run was written", rec.Code)
	}

	var status, output string
	if err := f.db.Get(&status,
		`SELECT status FROM scheduled_task_device_runs WHERE id = ?`, victimRun); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Get(&output,
		`SELECT COALESCE(output_log, '') FROM scheduled_task_device_runs WHERE id = ?`,
		victimRun); err != nil {
		t.Fatal(err)
	}
	if output == "done" {
		t.Error("d2's output_log was written by d1, an agent that does not own the run")
	}
	if status == "success" {
		t.Error("d2's run was marked successful by a device that never ran the script")
	}
}

// TestADeviceCanStillReportItsOwnResult keeps the ordinary path working. If
// this fails, no scheduled script ever reports a result.
func TestADeviceCanStillReportItsOwnResult(t *testing.T) {
	f := newRunFixture(t, []string{"d1", "d2"})

	for _, id := range []string{"d1", "d2"} {
		if rec := f.report(t, id, f.deviceRunFor(t, id), "success"); rec.Code != http.StatusOK {
			t.Fatalf("%s reporting its own run -> %d %s", id, rec.Code, rec.Body.String())
		}
	}
	if status, _ := f.parent(t); status != "completed" {
		t.Errorf("parent status = %q, want \"completed\"", status)
	}
}

// TestAnUnknownRunIsIndistinguishableFromAnotherDevicesRun keeps the refusal
// from becoming an oracle: if a missing run answered differently from someone
// else's, an agent could enumerate the fleet's run ids.
func TestAnUnknownRunIsIndistinguishableFromAnotherDevicesRun(t *testing.T) {
	f := newRunFixture(t, []string{"d1", "d2"})

	unknown := f.report(t, "d1", "no-such-run", "success")
	otherDevice := f.report(t, "d1", f.deviceRunFor(t, "d2"), "success")

	if unknown.Code != otherDevice.Code {
		t.Errorf("an unknown run answered %d but another device's run answered %d; "+
			"the difference lets an agent enumerate run ids", unknown.Code, otherDevice.Code)
	}
}

// TestTheTriggerResponseAgreesWithTheDatabase closes a smaller version of the
// same gap. TriggerSchedule stamped completed_at on the struct it returned
// without touching its status field, so a schedule targeting a device that does
// not exist answered status="running" alongside a non-null completed_at: a run
// claiming to be both still executing and already finished, in one object. The
// persisted row was already correct, which is exactly what let the two disagree
// about the same run.
func TestTheTriggerResponseAgreesWithTheDatabase(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "trigger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC()
	if _, err := database.Exec(`
		INSERT INTO script_templates (id, name, script_type, script_content, sha256_hash,
			timeout_seconds, created_by, created_at, updated_at)
		VALUES ('script-1','s','powershell','echo hi','abc',300,'u1',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	// Targeting a device that does not exist: the target resolves to nothing.
	if _, err := database.Exec(`
		INSERT INTO task_schedules (id, name, script_id, target_type, target_id,
			schedule_type, schedule_expr, is_enabled, created_by, created_at, updated_at)
		VALUES ('sched-z','s','script-1','device','no-such-device','once','now',0,'u1',?,?)`,
		now, now); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(database)
	run, err := NewScheduler(repo, stubHub{}).TriggerSchedule(context.Background(), "sched-z", "u1")
	if err != nil {
		t.Fatal(err)
	}

	var dbStatus string
	if err := database.Get(&dbStatus, `SELECT status FROM scheduled_task_runs WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if run.Status != dbStatus {
		t.Errorf("response status = %q but the row says %q: the operator is told two "+
			"different things about the same run", run.Status, dbStatus)
	}
	if run.CompletedAt != nil && run.Status == "running" {
		t.Errorf("status = \"running\" with completed_at = %v; a run cannot be both",
			*run.CompletedAt)
	}
}

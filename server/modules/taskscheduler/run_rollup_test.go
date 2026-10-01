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
	return newRunFixtureWithHub(t, deviceIDs, stubHub{})
}

// newRunFixtureWithHub takes the hub as a parameter because which devices the
// scheduler can reach is decided entirely by the hub, not the database: the
// dispatch loop asks hub.Online and skips the rest. A fixture that hard-codes
// an always-online hub can therefore never reach the offline branch.
func newRunFixtureWithHub(t *testing.T, deviceIDs []string, hub Hub) *runFixture {
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
	run, err := NewScheduler(repo, hub).TriggerSchedule(context.Background(), "sched-1", "u1")
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
	return f.reportRaw(t, deviceID, deviceRunID,
		`{"status":"`+status+`","exit_code":0,"output_log":"done"}`)
}

// reportRaw posts a body verbatim, so a test can use the agent's own wire
// format instead of this module's.
func (f *runFixture) reportRaw(t *testing.T, deviceID, deviceRunID, body string) *httptest.ResponseRecorder {
	t.Helper()
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

// TestAReportInTheExecutorsVocabularyClosesTheRun is the regression test for two
// mismatches that only a live agent could surface.
//
// The task scheduler dispatches "exec.run", which the agent handles in
// agent/shared/remoteexec -- the same executor remote-exec uses. It reports in
// its own words: status "completed", output under the key "output". This
// handler expected status in {success, failed} and the key "output_log", so a
// successful run wrote a device row reading "completed" and dropped the script
// output on the floor. "completed" is not in SyncRunStatus's terminal set, so
// the parent stayed 'running' with a NULL completed_at forever -- on the one
// path that had just demonstrably worked.
//
// Every test above posts the module's own vocabulary, which is why the whole
// suite was green while the feature did not work against a real agent.
func TestAReportInTheExecutorsVocabularyClosesTheRun(t *testing.T) {
	f := newRunFixture(t, []string{"d1"})

	rec := f.reportRaw(t, "d1", f.deviceRunFor(t, "d1"),
		`{"status":"completed","exit_code":0,"output":"live-agent-ok"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("report -> %d %s", rec.Code, rec.Body.String())
	}

	var status, output string
	if err := f.db.Get(&status,
		`SELECT status FROM scheduled_task_device_runs WHERE id = ?`,
		f.deviceRunFor(t, "d1")); err != nil {
		t.Fatal(err)
	}
	if status != "success" {
		t.Errorf("device run status = %q, want \"success\": \"completed\" is not in "+
			"the terminal set, so the rollup counts this run as still executing",
			status)
	}
	if err := f.db.Get(&output,
		`SELECT COALESCE(output_log, '') FROM scheduled_task_device_runs WHERE id = ?`,
		f.deviceRunFor(t, "d1")); err != nil {
		t.Fatal(err)
	}
	if output != "live-agent-ok" {
		t.Errorf("output_log = %q, want \"live-agent-ok\": the agent sends the key "+
			"\"output\" and this handler only ever read \"output_log\", so the "+
			"script's actual output was silently discarded", output)
	}

	parentStatus, completedAt := f.parent(t)
	if parentStatus != "completed" || completedAt == nil {
		t.Errorf("parent = %q completed_at=%v, want \"completed\" with a timestamp",
			parentStatus, completedAt)
	}
}

// TestATimedOutReportIsRecordedAsFailed: "timeout" is the executor's word, and it
// has to reach this table as a failure rather than a status nothing rolls up.
func TestATimedOutReportIsRecordedAsFailed(t *testing.T) {
	f := newRunFixture(t, []string{"d1"})

	if rec := f.reportRaw(t, "d1", f.deviceRunFor(t, "d1"),
		`{"status":"timeout","exit_code":-1,"error_message":"deadline exceeded"}`); rec.Code != http.StatusOK {
		t.Fatalf("report -> %d %s", rec.Code, rec.Body.String())
	}

	if status, _ := f.parent(t); status != "failed" {
		t.Errorf("parent status = %q after the device timed out, want \"failed\"", status)
	}
}

// offlineHub reports every device as disconnected, which is what a fleet of
// sleeping laptops looks like at 2am.
type offlineHub struct{ stubHub }

func (offlineHub) Online(string) bool { return false }

// TestARunAgainstOnlyOfflineDevicesStillFinishes is the regression test for the
// branch that had no way to close.
//
// The offline branch wrote 'failed, device is offline' to the device run and
// moved on. SyncRunStatus is only ever called from reportTaskResult, and a
// device that was never given the command cannot report. So the parent stayed
// 'running' with a NULL completed_at permanently: an operator who ran a script
// against a company whose machines were asleep saw 'running' forever, with
// every device row already reading failed, and nothing that could ever move it.
func TestARunAgainstOnlyOfflineDevicesStillFinishes(t *testing.T) {
	f := newRunFixtureWithHub(t, []string{"d1", "d2", "d3"}, offlineHub{})

	status, completedAt := f.parent(t)
	if status != "failed" {
		t.Errorf("parent status = %q with every device offline, want \"failed\": no "+
			"endpoint can run the script, so nothing about this run succeeded",
			status)
	}
	if completedAt == nil {
		t.Error("completed_at is NULL on a run that can never progress; the run " +
			"history keeps listing it as in progress forever")
	}
}

// TestAMixedRunIsNotClosedEarly keeps the rollup honest in the other direction.
// One device reachable and two asleep is still one outstanding device, so the
// run must stay running until the reachable one reports back.
func TestAMixedRunIsNotClosedEarly(t *testing.T) {
	f := newRunFixtureWithHub(t, []string{"d1", "d2"}, oneOnlineHub{})

	if status, _ := f.parent(t); status != "running" {
		t.Errorf("parent status = %q with one device still executing, want \"running\"", status)
	}
	if rec := f.report(t, "d1", f.deviceRunFor(t, "d1"), "success"); rec.Code != http.StatusOK {
		t.Fatalf("d1 -> %d %s", rec.Code, rec.Body.String())
	}
	if status, completedAt := f.parent(t); status != "completed" || completedAt == nil {
		t.Errorf("parent = %q completed_at=%v after the last device reported, want "+
			"\"completed\" with a timestamp", status, completedAt)
	}
}

// oneOnlineHub reaches exactly the first device it is asked about, so a fixture
// can mix reachable and unreachable endpoints in one run.
type oneOnlineHub struct{ stubHub }

func (oneOnlineHub) Online(deviceID string) bool { return deviceID == "d1" }

// TestATriggerAgainstOfflineDevicesReportsTheFinishedRunNotARunningOne closes
// the response side. The returned struct was hand-edited from a copy taken
// before the dispatch loop, so even with the rollup in place it went back to the
// operator saying 'running' while the row said 'failed' -- the same split the
// completed_at bug produced, one branch over.
func TestATriggerAgainstOfflineDevicesReportsTheFinishedRunNotARunningOne(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "offline-trigger.db"))
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
	if _, err := database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
			device_secret_hash, enrolled_at, created_at, updated_at)
		VALUES ('d1','d1','windows','11','1.0.0','offline','x',?,?,?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		INSERT INTO task_schedules (id, name, script_id, target_type, target_id,
			schedule_type, schedule_expr, is_enabled, created_by, created_at, updated_at)
		VALUES ('sched-z','s','script-1','all','','interval','60',1,'u1',?,?)`,
		now, now); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(database)
	run, err := NewScheduler(repo, offlineHub{}).TriggerSchedule(context.Background(), "sched-z", "u1")
	if err != nil {
		t.Fatal(err)
	}

	var dbStatus string
	if err := database.Get(&dbStatus, `SELECT status FROM scheduled_task_runs WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if run.Status != dbStatus {
		t.Errorf("response status = %q but the row says %q", run.Status, dbStatus)
	}
	if run.Status != "failed" {
		t.Errorf("trigger response = %q, want \"failed\": the operator clicked run "+
			"and was told nothing was running, while the run list said the opposite",
			run.Status)
	}
}

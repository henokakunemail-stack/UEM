package agentupdate

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
	"github.com/jmoiron/sqlx"
)

type discardAuditor struct{}

func (discardAuditor) Log(context.Context, string, string, string, string, map[string]string) error {
	return nil
}

// Two enrolled devices, one update task belonging to B, and a router wired the
// way production wires it.
func newReportFixture(t *testing.T) (http.Handler, *sqlx.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "report.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC()
	seed := func(id, secret string) {
		if _, err := database.Exec(`
			INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
				device_secret_hash, enrolled_at, created_at, updated_at)
			VALUES (?, ?, 'windows', '11', '1.0.0', 'online', ?, ?, ?, ?)`,
			id, id, devicemgmt.HashToken(secret), now, now, now); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("device-A", "secret-A")
	seed("device-B", "secret-B")

	repo := NewRepository(database)
	h := NewHandler(repo, nil, devicemgmt.NewRepository(database), discardAuditor{},
		t.TempDir(), func(next http.Handler) http.Handler { return next }, "")
	if err := repo.CreateUpdateTask(context.Background(), &DeviceUpdateTask{
		ID: "task-B", DeviceID: "device-B",
		FromVersion: "1.0.0", TargetVersion: "2.0.0", Status: "pending",
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	r := chi.NewRouter()
	h.Register(r)
	return r, database
}

func report(t *testing.T, r http.Handler, deviceID, secret, urlDevice, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/agent/devices/"+urlDevice+"/update/report", bytes.NewBufferString(body))
	req.Header.Set("X-Device-Id", deviceID)
	req.Header.Set("X-Device-Secret", secret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestAnAgentCannotCompleteAnotherDevicesRollout is the regression test for a
// cross-device write that a comment claimed was already closed.
//
// The handler authenticated the agent and then took the device id from the URL,
// which is attacker-controlled, without ever comparing it to the identity that
// was authenticated. Any enrolled device could name any other device, mark its
// rollout successful and overwrite its agent_version:
//
//	POST /api/agent/devices/device-B/update/report
//	X-Device-Id: device-A   X-Device-Secret: secret-A
//	{"task_id":"task-B","status":"success","target_version":"9.9.9"}
//
// The old comment above this handler said that without the authentication check
// "any client that could reach the port could mark a rollout successful and
// overwrite devices.agent_version for any device ID" -- which described the
// anonymous case only. An enrolled agent on an unrelated branch reaches the same
// outcome, and the fleet's version inventory is what that rewrites.
func TestAnAgentCannotCompleteAnotherDevicesRollout(t *testing.T) {
	r, database := newReportFixture(t)

	rec := report(t, r, "device-A", "secret-A", "device-B",
		`{"task_id":"task-B","status":"success","target_version":"9.9.9"}`)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; another device's rollout was written", rec.Code)
	}

	var version string
	if err := database.Get(&version, `SELECT agent_version FROM devices WHERE id = 'device-B'`); err != nil {
		t.Fatal(err)
	}
	if version != "1.0.0" {
		t.Errorf("device-B agent_version = %q, want \"1.0.0\": a device that did not run the "+
			"update was recorded as having run it", version)
	}

	var status string
	if err := database.Get(&status, `SELECT status FROM device_update_tasks WHERE id = 'task-B'`); err != nil {
		t.Fatal(err)
	}
	if status == "success" {
		t.Error("device-B's task was marked successful by device-A")
	}
}

// TestAnAgentCannotAdvanceAnotherDevicesTask covers the second half: the task id
// arrives in the request body, so checking the device in the path is not enough.
// The URL can name the caller's own device while the body names someone else's
// task.
func TestAnAgentCannotAdvanceAnotherDevicesTask(t *testing.T) {
	r, database := newReportFixture(t)

	rec := report(t, r, "device-A", "secret-A", "device-A",
		`{"task_id":"task-B","status":"success","target_version":"9.9.9"}`)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	var status string
	if err := database.Get(&status, `SELECT status FROM device_update_tasks WHERE id = 'task-B'`); err != nil {
		t.Fatal(err)
	}
	if status == "success" {
		t.Error("device-B's task was advanced by device-A through its own URL")
	}
}

// TestADeviceCanStillReportItsOwnRollout keeps the scoping from breaking the
// path every update actually takes. If this fails, no rollout ever completes.
func TestADeviceCanStillReportItsOwnRollout(t *testing.T) {
	r, database := newReportFixture(t)

	rec := report(t, r, "device-B", "secret-B", "device-B",
		`{"task_id":"task-B","status":"success","target_version":"2.0.0"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: the owner must still be able to report "+
			"(body: %s)", rec.Code, rec.Body.String())
	}
	var version string
	if err := database.Get(&version, `SELECT agent_version FROM devices WHERE id = 'device-B'`); err != nil {
		t.Fatal(err)
	}
	if version != "2.0.0" {
		t.Errorf("device-B agent_version = %q, want \"2.0.0\"", version)
	}
}

// TestAnUnauthenticatedClientStillCannotReport keeps the original check in
// place: the new comparison must sit on top of it, not replace it.
func TestAnUnauthenticatedClientStillCannotReport(t *testing.T) {
	r, database := newReportFixture(t)

	rec := report(t, r, "device-B", "wrong-secret", "device-B",
		`{"task_id":"task-B","status":"success","target_version":"9.9.9"}`)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	var version string
	if err := database.Get(&version, `SELECT agent_version FROM devices WHERE id = 'device-B'`); err != nil {
		t.Fatal(err)
	}
	if version != "1.0.0" {
		t.Errorf("agent_version = %q, want \"1.0.0\"", version)
	}
}

package patchmgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

func jobError(t *testing.T, h *Handler, jobID string) string {
	t.Helper()
	var msg string
	if err := h.repo.db.Get(&msg, `SELECT COALESCE(error_message, '') FROM patch_install_jobs WHERE id = ?`, jobID); err != nil {
		t.Fatal(err)
	}
	return msg
}

// A retried or late report used to overwrite a finished job and restamp it.
func TestAFinishedJobIsNotRewritten(t *testing.T) {
	h, database := patchFixture(t)
	if rec := reportInstall(t, h, "dev-a", "secret-a", "job-a", JobStatusCompleted); rec.Code != http.StatusOK {
		t.Fatalf("first report = %d", rec.Code)
	}
	rec := reportInstall(t, h, "dev-a", "secret-a", "job-a", JobStatusFailed)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ignored") {
		t.Fatalf("report for a finished job = %d %s, want 200 ignored", rec.Code, rec.Body.String())
	}
	if status, _ := jobRow(t, database, "job-a"); status != JobStatusCompleted {
		t.Fatalf("job after a late report = %q, want completed", status)
	}
	database.MustExec(`INSERT INTO patch_install_jobs (id, device_id, patch_ids, status) VALUES ('job-a2','dev-a','[]','dispatched')`)
	if rec := reportInstall(t, h, "dev-a", "secret-a", "job-a2", "exploded"); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown status = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if status, _ := jobRow(t, database, "job-a2"); status != "dispatched" {
		t.Fatalf("job after an unknown status = %q, want it untouched", status)
	}
}

// A job whose agent died or whose result was lost said 'dispatched' forever.
func TestASilentJobIsFailedAndALateResultStillLands(t *testing.T) {
	h, database := patchFixture(t)
	old := time.Now().UTC().Add(-JobNoReportAfter - time.Minute)
	database.MustExec(`UPDATE patch_install_jobs SET status = 'dispatched', started_at = ?, updated_at = ? WHERE id = 'job-a'`, old, old)
	database.MustExec(`UPDATE patch_install_jobs SET status = 'dispatched', started_at = ?, updated_at = ? WHERE id = 'job-b'`, time.Now().UTC(), time.Now().UTC())

	n, err := h.repo.AbandonSilentJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reaped %d jobs, want 1 (job-b is recent)", n)
	}
	if status, _ := jobRow(t, database, "job-a"); status != JobStatusFailed || !strings.HasPrefix(jobError(t, h, "job-a"), "no_report") {
		t.Fatalf("silent job = %q %q, want failed no_report", status, jobError(t, h, "job-a"))
	}

	// The agent was only slow: its real result replaces the sweep's guess.
	if rec := reportInstall(t, h, "dev-a", "secret-a", "job-a", JobStatusCompleted); rec.Code != http.StatusOK {
		t.Fatalf("late result = %d %s", rec.Code, rec.Body.String())
	}
	if status, _ := jobRow(t, database, "job-a"); status != JobStatusCompleted {
		t.Fatalf("job after the late real result = %q, want completed", status)
	}
}

// patch.install goes out under the job id; a refusal used to land in
// agent_commands, match nothing, and be dropped.
func TestAnAgentRefusalFailsTheJob(t *testing.T) {
	h, database := patchFixture(t)
	if h.HandleCommandResult(context.Background(), "dev-b", "job-a", "failed", "x") {
		t.Fatal("another device's result claimed the job")
	}
	if !h.HandleCommandResult(context.Background(), "dev-a", "job-a", "failed", "invalid patch.install payload") {
		t.Fatal("the refusal was not claimed")
	}
	if status, _ := jobRow(t, database, "job-a"); status != JobStatusFailed || !strings.Contains(jobError(t, h, "job-a"), "invalid patch.install payload") {
		t.Fatalf("job after refusal = %q %q", status, jobError(t, h, "job-a"))
	}
}

// The reboot count only ever grew: nothing cleared the flag after a reboot.
func TestAScanSayingNoRebootClearsTheFlag(t *testing.T) {
	h, database := patchFixture(t)
	database.MustExec(`INSERT INTO device_patches (device_id, patch_id, installed_state, reboot_required) VALUES
		('dev-a','KB1','installed',1)`)

	scan := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/agent/patches/scan-report", strings.NewReader(body))
		req.Header.Set("X-Device-Id", "dev-a")
		req.Header.Set("X-Device-Secret", "secret-a")
		rec := httptest.NewRecorder()
		h.reportScanResults(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("scan report = %d %s", rec.Code, rec.Body.String())
		}
	}
	flag := func() int {
		var n int
		_ = database.Get(&n, `SELECT reboot_required FROM device_patches WHERE device_id = 'dev-a' AND patch_id = 'KB1'`)
		return n
	}

	scan(`{"patches":[]}`) // an older agent: no answer
	if flag() != 1 {
		t.Fatal("a scan without reboot_pending cleared the flag")
	}
	scan(`{"patches":[],"reboot_pending":true}`)
	if flag() != 1 {
		t.Fatal("reboot_pending=true cleared the flag")
	}
	scan(`{"patches":[],"reboot_pending":false}`)
	if flag() != 0 {
		t.Fatal("reboot_pending=false left the flag set")
	}
}

type busyHub struct{}

func (busyHub) Online(string) bool         { return true }
func (busyHub) SendTo(string, []byte) bool { return false }

// A send that fails leaves a job nobody will ever report on. It used to stay
// 'dispatched' beside the 409.
func TestAnUndeliveredInstallFailsTheJob(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "patch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC()
	database.MustExec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES ('u1','op','x','admin',?,?)`, now, now)
	database.MustExec(`INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status, device_secret_hash, enrolled_at, created_at, updated_at)
		VALUES ('dev-a','PC-A','windows','11','1.0.0','online','h',?,?,?)`, now, now, now)

	h := NewHandler(NewRepository(database), busyHub{}, nopAudit{}, nil, nil, devicemgmt.NewRepository(database))
	body, _ := json.Marshal(InstallRequest{PatchIDs: []string{"KB1"}})
	req := httptest.NewRequest(http.MethodPost, "/api/devices/dev-a/patches/install", bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "dev-a")
	req = req.WithContext(context.WithValue(context.WithValue(req.Context(), chi.RouteCtxKey, rctx), auth.CtxUserID, "u1"))
	rec := httptest.NewRecorder()
	h.installPatches(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("install over a busy socket = %d %s, want 409", rec.Code, rec.Body.String())
	}
	var status string
	if err := database.Get(&status, `SELECT status FROM patch_install_jobs WHERE device_id = 'dev-a'`); err != nil {
		t.Fatal(err)
	}
	if status != JobStatusFailed {
		t.Fatalf("job after an undelivered send = %q, want failed", status)
	}
}

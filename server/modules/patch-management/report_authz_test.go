package patchmgmt

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

// patchFixture builds a handler over a real database holding two devices with
// real secrets, and one install job belonging to each.
func patchFixture(t *testing.T) (*Handler, *sqlx.DB) {
	t.Helper()

	database, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	// FindBySecretHash does `SELECT *`, so this table has to carry every column
	// the Device struct scans into, not just the ones this test reads. Writing it
	// out by hand is a way to notice that the struct is the contract: a missing
	// nullable column here reads as a scan error, which the handler reports as
	// invalid credentials, and a missing one in a test fixture looks exactly like
	// an authentication failure.
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
	database.MustExec(`CREATE TABLE patch_install_jobs (
		id TEXT PRIMARY KEY,
		device_id TEXT NOT NULL,
		patch_ids TEXT NOT NULL DEFAULT '[]',
		status TEXT NOT NULL DEFAULT 'pending',
		reboot_required INTEGER NOT NULL DEFAULT 0,
		output_log TEXT,
		error_message TEXT,
		started_at DATETIME,
		completed_at DATETIME
	);`)
	database.MustExec(`CREATE TABLE device_patches (
		device_id TEXT NOT NULL,
		patch_id TEXT NOT NULL,
		installed_state TEXT NOT NULL DEFAULT 'missing',
		reboot_required INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME,
		PRIMARY KEY (device_id, patch_id)
	);`)

	database.MustExec(`INSERT INTO devices (id, hostname, os_name, device_secret_hash, enrolled_at, created_at, updated_at) VALUES
		('dev-a','PC-A','windows',?, '2026-01-01','2026-01-01','2026-01-01'),
		('dev-b','PC-B','windows',?, '2026-01-01','2026-01-01','2026-01-01')`,
		devicemgmt.HashToken("secret-a"), devicemgmt.HashToken("secret-b"))
	database.MustExec(`INSERT INTO patch_install_jobs (id, device_id, patch_ids) VALUES
		('job-a','dev-a','[]'), ('job-b','dev-b','[]')`)

	return &Handler{
		repo:    NewRepository(database),
		devices: devicemgmt.NewRepository(database),
		audit:   nopAudit{},
	}, database
}

func reportInstall(t *testing.T, h *Handler, deviceID, secret string, jobID, status string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(PatchInstallReport{JobID: jobID, Status: status, OutputLog: "attacker output"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/patch/install/result", bytes.NewReader(body))
	req.Header.Set("X-Device-Id", deviceID)
	req.Header.Set("X-Device-Secret", secret)
	rctx := chi.NewRouteContext()
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.reportInstallResult(rec, req)
	return rec
}

func jobRow(t *testing.T, database *sqlx.DB, jobID string) (status, outputLog string) {
	t.Helper()
	if err := database.Get(&status, `SELECT status FROM patch_install_jobs WHERE id = ?`, jobID); err != nil {
		t.Fatal(err)
	}
	// output_log is NULL until something reports; sqlx cannot scan NULL into a
	// string, and the NULL is the state this test asserts is preserved.
	if err := database.Get(&outputLog,
		`SELECT COALESCE(output_log, '<null>') FROM patch_install_jobs WHERE id = ?`, jobID); err != nil {
		t.Fatal(err)
	}
	return status, outputLog
}

// TestAnAgentCannotCompleteAnotherDevicesInstallJob is the regression test for a
// cross-device write.
//
// reportInstallResult authenticated the caller properly: it read X-Device-Id
// and X-Device-Secret and compared the secret hash's device to the id it
// claimed. Then it took the job id from the request body and passed it to
// UpdateInstallJobResult, whose UPDATE was `WHERE id = ?` with no device term
// at all.
//
// So the authentication decided nothing. Any enrolled agent in the fleet could
// POST another device's job id with a fabricated status, and the server wrote
// it: the job's status, output_log, error_message and completed_at were all
// overwritten, and the audit row recorded the *attacking* device as the actor.
// The completed case was worse, because it also flipped device_patches rows to
// 'installed' for the victim.
func TestAnAgentCannotCompleteAnotherDevicesInstallJob(t *testing.T) {
	h, database := patchFixture(t)

	rec := reportInstall(t, h, "dev-a", "secret-a", "job-b", JobStatusCompleted)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: dev-a reported on dev-b's job and the "+
			"server accepted it (body: %s)", rec.Code, rec.Body.String())
	}

	status, output := jobRow(t, database, "job-b")
	if status != "pending" {
		t.Errorf("job-b status = %q, want \"pending\": another device's install was "+
			"resolved by an agent that was never given it", status)
	}
	if output == "attacker output" {
		t.Error("job-b's output_log was overwritten by the reporting device")
	}
}

// TestAnAgentCannotFailAnotherDevicesInstallJob: the same write with the other
// status. Marking a peer's install failed is as damaging as marking it
// completed -- it is what the console uses to show a rollout as broken.
func TestAnAgentCannotFailAnotherDevicesInstallJob(t *testing.T) {
	h, database := patchFixture(t)

	rec := reportInstall(t, h, "dev-a", "secret-a", "job-b", JobStatusFailed)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
	if status, _ := jobRow(t, database, "job-b"); status != "pending" {
		t.Errorf("job-b status = %q, want \"pending\"", status)
	}
}

// TestAnUnknownJobIsIndistinguishableFromAnotherDevicesJob: the refusal must
// not double as an existence oracle, or an agent could enumerate the fleet's
// job ids from the responses.
func TestAnUnknownJobIsIndistinguishableFromAnotherDevicesJob(t *testing.T) {
	h, _ := patchFixture(t)

	otherDevice := reportInstall(t, h, "dev-a", "secret-a", "job-b", JobStatusCompleted)
	noSuchJob := reportInstall(t, h, "dev-a", "secret-a", "job-does-not-exist", JobStatusCompleted)

	if otherDevice.Code != noSuchJob.Code {
		t.Errorf("another device's job returned %d but a nonexistent one returned %d; "+
			"the difference is an enumeration oracle", otherDevice.Code, noSuchJob.Code)
	}
	if otherDevice.Body.String() != noSuchJob.Body.String() {
		t.Errorf("bodies differ:\n  other: %s\n  absent: %s",
			otherDevice.Body.String(), noSuchJob.Body.String())
	}
}

// TestTheOwnerCanStillReportItsOwnJob: the guard has to be ownership, not a
// blanket refusal.
func TestTheOwnerCanStillReportItsOwnJob(t *testing.T) {
	h, database := patchFixture(t)

	rec := reportInstall(t, h, "dev-a", "secret-a", "job-a", JobStatusCompleted)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: dev-a could not report on its own job "+
			"(body: %s)", rec.Code, rec.Body.String())
	}
	if status, _ := jobRow(t, database, "job-a"); status != JobStatusCompleted {
		t.Errorf("job-a status = %q, want %q", status, JobStatusCompleted)
	}
}

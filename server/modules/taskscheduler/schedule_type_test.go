package taskscheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

func scheduleFixture(t *testing.T) (*sqlx.DB, chi.Router) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	repo := NewRepository(d)
	// The scheduler and the device lookup are not on the path this test
	// exercises, and the RBAC layer is left off so the 400 comes from the
	// schedule_type check rather than from a missing admin claim.
	h := NewHandler(repo, NewScheduler(repo, stubHub{}), nopAuditor{}, passthroughAuth, nil)

	r := chi.NewRouter()
	h.Register(r)
	return d, r
}

func passthroughAuth(next http.Handler) http.Handler { return next }

func TestAScheduleTypeTheSchedulerDoesNotRunIsRefused(t *testing.T) {
	_, r := scheduleFixture(t)

	for _, scheduleType := range []string{"cron", "once", "INTERVAL", "daily"} {
		t.Run("type="+scheduleType, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"name":          "nightly",
				"script_id":     "no-such-script",
				"target_type":   "device",
				"target_id":     "dev-1",
				"schedule_type": scheduleType,
				"schedule_expr": "60",
				"is_enabled":    true,
			})
			req := httptest.NewRequest(http.MethodPost, "/api/schedules", strings.NewReader(string(body)))
			ctx := context.WithValue(req.Context(), auth.CtxUserID, "admin-1")
			req = req.WithContext(rbac.WithRole(ctx, rbac.RoleAdmin))

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			// The script does not exist either, so this only proves the
			// schedule_type check runs first. That ordering is deliberate: the
			// operator is told what is wrong with their schedule rather than
			// sent to look up a script id that may be fine.
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("schedule_type %q returned %d, want 400", scheduleType, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "schedule_type") {
				t.Errorf("schedule_type %q: response does not name the field: %s", scheduleType, rec.Body.String())
			}
		})
	}
}

// The one type the scheduler does run still has to get through, otherwise the
// check above has simply disabled the feature.
func TestTheTypeTheSchedulerRunsIsAccepted(t *testing.T) {
	if !isRunnableScheduleType("interval") {
		t.Error("interval is rejected; the scheduler fires only this type, so nothing would ever run")
	}
}

// A partial update has to mean "change what was sent". The request struct is all
// pointers, so an omitted key decodes to nil and the stored value survives; with
// plain values the same body would blank the script, the target, the expression
// and disable the schedule, and the row would look unchanged in the console.
func TestUpdateScheduleKeepsFieldsTheBodyOmitted(t *testing.T) {
	database, r := scheduleFixture(t)

	now := time.Now().UTC()
	if _, err := database.Exec(`
		INSERT INTO script_templates (id, name, script_type, script_content, sha256_hash,
			timeout_seconds, created_by, created_at, updated_at)
		VALUES ('script-1','patch script','powershell','echo hi','abc',300,'u1',?,?)`, now, now); err != nil {
		t.Fatalf("seed script: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO task_schedules (id, name, description, script_id, target_type, target_id,
			schedule_type, schedule_expr, is_enabled, created_by, created_at, updated_at)
		VALUES ('sched-1','original name','original note','script-1','device','dev-1',
			'interval','60',1,'u1',?,?)`, now, now); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"name": "renamed", "is_enabled": false})
	req := httptest.NewRequest(http.MethodPut, "/api/schedules/sched-1", strings.NewReader(string(body)))
	ctx := context.WithValue(req.Context(), auth.CtxUserID, "admin-1")
	req = req.WithContext(rbac.WithRole(ctx, rbac.RoleAdmin))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT returned %d: %s", rec.Code, rec.Body.String())
	}

	var updated TaskSchedule
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if updated.Name != "renamed" || updated.IsEnabled != false {
		t.Errorf("sent fields not applied: name=%q is_enabled=%v", updated.Name, updated.IsEnabled)
	}

	// Every field the body omitted has to read back as it was stored, not as the
	// zero value decoding would have written over it.
	if updated.Description != "original note" {
		t.Errorf("description was blanked: got %q, want %q", updated.Description, "original note")
	}
	if updated.ScriptID != "script-1" || updated.ScriptName != "patch script" {
		t.Errorf("script was lost: id=%q name=%q", updated.ScriptID, updated.ScriptName)
	}
	if updated.TargetType != "device" || updated.TargetID != "dev-1" {
		t.Errorf("target was blanked: type=%q id=%q", updated.TargetType, updated.TargetID)
	}
	if updated.ScheduleType != "interval" || updated.ScheduleExpr != "60" {
		t.Errorf("schedule was blanked: type=%q expr=%q", updated.ScheduleType, updated.ScheduleExpr)
	}

	// The row in the database, not just the response, since a handler could
	// report the fetched values while writing zeros.
	var stored TaskSchedule
	if err := database.GetContext(context.Background(), &stored,
		`SELECT script_id, target_id, schedule_expr FROM task_schedules WHERE id = 'sched-1'`); err != nil {
		t.Fatalf("read back schedule: %v", err)
	}
	if stored.ScriptID != "script-1" || stored.TargetID != "dev-1" || stored.ScheduleExpr != "60" {
		t.Errorf("database row was blanked: %+v", stored)
	}
}

// The script check createSchedule has is missing on the update path, so a bad
// id used to be stored and then fail GetScriptByID on every fire from then on.
func TestUpdateScheduleRejectsAScriptThatDoesNotExist(t *testing.T) {
	database, r := scheduleFixture(t)

	now := time.Now().UTC()
	if _, err := database.Exec(`
		INSERT INTO script_templates (id, name, script_type, script_content, sha256_hash,
			timeout_seconds, created_by, created_at, updated_at)
		VALUES ('script-1','real script','powershell','echo hi','abc',300,'u1',?,?)`, now, now); err != nil {
		t.Fatalf("seed script: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO task_schedules (id, name, script_id, target_type, target_id,
			schedule_type, schedule_expr, is_enabled, created_by, created_at, updated_at)
		VALUES ('sched-1','s','script-1','device','dev-1','interval','60',1,'u1',?,?)`, now, now); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"script_id": "no-such-script"})
	req := httptest.NewRequest(http.MethodPut, "/api/schedules/sched-1", strings.NewReader(string(body)))
	ctx := context.WithValue(req.Context(), auth.CtxUserID, "admin-1")
	req = req.WithContext(rbac.WithRole(ctx, rbac.RoleAdmin))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT with a bad script_id returned %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "script does not exist") {
		t.Errorf("response does not say the script is missing: %s", rec.Body.String())
	}

	var stored TaskSchedule
	if err := database.GetContext(context.Background(), &stored,
		`SELECT script_id FROM task_schedules WHERE id = 'sched-1'`); err != nil {
		t.Fatalf("read back schedule: %v", err)
	}
	if stored.ScriptID != "script-1" {
		t.Errorf("bad script_id was stored: got %q, want %q", stored.ScriptID, "script-1")
	}
}

// TestAPartialScriptUpdateKeepsTheFieldsTheClientDidNotSend is the regression
// test for an implicit full overwrite.
//
// updateScript decoded the create request, whose fields are plain values, and
// assigned all of them onto the fetched row. JSON decoding cannot tell an
// omitted field from one sent as its zero value, so a body naming only the name
// blanked the description, the type, the body and the args, and zeroed the
// timeout. The console sends full bodies, so this slept for as long as no other
// client hit the route -- and the hash hid it: UpdateScript recomputes SHA256
// over whatever content it is handed, so a row emptied by the same write still
// matched its own hash. An operator renaming a script would have silently
// deleted the code the fleet was scheduled to run.
func TestAPartialScriptUpdateKeepsTheFieldsTheClientDidNotSend(t *testing.T) {
	d, r := scheduleFixture(t)

	now := time.Now().UTC()
	const content = "Remove-Item tmp"
	if _, err := d.Exec(`
		INSERT INTO script_templates (id, name, description, script_type, script_content,
			sha256_hash, default_args, timeout_seconds, created_by, created_at, updated_at)
		VALUES ('script-1','Disk cleanup','Clears temp dirs','powershell',?,?, '-Verbose',900,'u1',?,?)`,
		content, CalculateSHA256(content), now, now); err != nil {
		t.Fatal(err)
	}

	body := `{"name":"Disk cleanup v2"}`
	req := httptest.NewRequest(http.MethodPut, "/api/scripts/script-1", strings.NewReader(body))
	ctx := context.WithValue(req.Context(), auth.CtxUserID, "u1")
	req = req.WithContext(rbac.WithRole(ctx, rbac.RoleAdmin))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("partial update -> %d %s", rec.Code, rec.Body.String())
	}

	var stored struct {
		Name           string `db:"name"`
		Description    string `db:"description"`
		ScriptType     string `db:"script_type"`
		ScriptContent  string `db:"script_content"`
		SHA256Hash     string `db:"sha256_hash"`
		DefaultArgs    string `db:"default_args"`
		TimeoutSeconds int    `db:"timeout_seconds"`
	}
	if err := d.Get(&stored, `SELECT name, description, script_type, script_content,
		sha256_hash, default_args, timeout_seconds FROM script_templates WHERE id = ?`,
		"script-1"); err != nil {
		t.Fatal(err)
	}

	if stored.Name != "Disk cleanup v2" {
		t.Errorf("name = %q, want the one field the body named", stored.Name)
	}
	if stored.ScriptContent != "Remove-Item tmp" {
		t.Errorf("script_content = %q, want the stored script: a rename blanked the code the fleet runs", stored.ScriptContent)
	}
	if stored.SHA256Hash != CalculateSHA256(content) {
		t.Errorf("sha256_hash = %q, want the stored script's hash: a rehash over emptied content matches itself, so this column reports nothing", stored.SHA256Hash)
	}
	if stored.ScriptType != "powershell" {
		t.Errorf("script_type = %q, want \"powershell\"", stored.ScriptType)
	}
	if stored.DefaultArgs != "-Verbose" {
		t.Errorf("default_args = %q, want \"-Verbose\"", stored.DefaultArgs)
	}
	if stored.TimeoutSeconds != 900 {
		t.Errorf("timeout_seconds = %d, want 900: a zero is rewritten to the 300 default by UpdateScript, hiding the overwrite", stored.TimeoutSeconds)
	}
	if stored.Description != "Clears temp dirs" {
		t.Errorf("description = %q, want \"Clears temp dirs\"", stored.Description)
	}
}

// A schedule pointing at a script that does not exist can never fire: TriggerSchedule
// resolves the script and fails on every run from then on, while the console shows the
// schedule as armed. createSchedule refuses that; update is the other path the column is
// written, so it refuses it too.
func TestUpdateScheduleRefusesAScriptThatDoesNotExist(t *testing.T) {
	database, r := scheduleFixture(t)

	now := time.Now().UTC()
	if _, err := database.Exec(`
		INSERT INTO script_templates (id, name, script_type, script_content, sha256_hash,
			timeout_seconds, created_by, created_at, updated_at)
		VALUES ('script-1','patch script','powershell','echo hi','abc',300,'u1',?,?)`, now, now); err != nil {
		t.Fatalf("seed script: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO task_schedules (id, name, description, script_id, target_type, target_id,
			schedule_type, schedule_expr, is_enabled, created_by, created_at, updated_at)
		VALUES ('sched-1','nightly','note','script-1','device','dev-1','interval','60',1,'u1',?,?)`, now, now); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"script_id": "no-such-script"})
	req := httptest.NewRequest(http.MethodPut, "/api/schedules/sched-1", strings.NewReader(string(body)))
	ctx := context.WithValue(req.Context(), auth.CtxUserID, "admin-1")
	req = req.WithContext(rbac.WithRole(ctx, rbac.RoleAdmin))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT with a missing script_id returned %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// The stored script_id must be unchanged; a refusal that still wrote the bad
	// reference would be worse than no check at all.
	var stored struct {
		ScriptID string `db:"script_id"`
	}
	if err := database.GetContext(context.Background(), &stored,
		`SELECT script_id FROM task_schedules WHERE id = 'sched-1'`); err != nil {
		t.Fatalf("read back schedule: %v", err)
	}
	if stored.ScriptID != "script-1" {
		t.Errorf("script_id was rewritten to %q before the validation failed", stored.ScriptID)
	}
}

// A script_id check that ran before the schedule was known to exist would answer 400 for
// a schedule that is not there at all. The 404 is what an operator needs to see, since it
// names the thing that is actually missing.
func TestUpdateScheduleNamesTheMissingScheduleBeforeComplainingAboutTheScript(t *testing.T) {
	_, r := scheduleFixture(t)

	// No schedule is seeded and no script is seeded, so both checks could fire.
	body, _ := json.Marshal(map[string]any{"script_id": "no-such-script"})
	req := httptest.NewRequest(http.MethodPut, "/api/schedules/does-not-exist", strings.NewReader(string(body)))
	ctx := context.WithValue(req.Context(), auth.CtxUserID, "admin-1")
	req = req.WithContext(rbac.WithRole(ctx, rbac.RoleAdmin))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT on a missing schedule returned %d, want 404 naming the schedule: %s",
			rec.Code, rec.Body.String())
	}
}

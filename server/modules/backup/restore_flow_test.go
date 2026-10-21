package backup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// waitIdle waits until no restore holds the lock.
func waitIdle(t *testing.T, h *Handler) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.restoring.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the restore lock was never released")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type recordingAudit struct {
	mu      sync.Mutex
	actions []string
	details []map[string]string
}

func (a *recordingAudit) Log(_ context.Context, _, actorID, action, _ string, details map[string]string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, actorID+":"+action)
	a.details = append(a.details, details)
	return nil
}

func (a *recordingAudit) count(action string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, x := range a.actions {
		if strings.HasSuffix(x, ":"+action) {
			n++
		}
	}
	return n
}

func restoreEnv(t *testing.T) (*Handler, *chi.Mux, *sqlx.DB, config.Config) {
	t.Helper()
	database, dbPath, backupDir := setupTestDB(t)
	t.Cleanup(func() { _ = database.Close() })
	cfg := config.Config{DBPath: dbPath, BackupDir: backupDir, DBDriver: "sqlite"}
	h := NewHandler(cfg, database, &mockAuditor{}, adminAuthMw)
	r := chi.NewRouter()
	h.Register(r)
	return h, r, database, cfg
}

func createBackup(t *testing.T, r *chi.Mux) string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/system/backup/create", nil))
	var created BackupFileDTO
	_ = json.NewDecoder(rec.Body).Decode(&created)
	if created.Filename == "" {
		t.Fatalf("create backup: %d %s", rec.Code, rec.Body.String())
	}
	return created.Filename
}

func postRestore(r *chi.Mux, filename string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/system/backup/restore/"+filename, nil))
	return rec
}

// Without a safety snapshot there is nothing to roll back to, so the restore
// must not start. It used to log a warning and go ahead.
func TestRestoreIsRefusedWhenTheSafetySnapshotFails(t *testing.T) {
	h, r, database, _ := restoreEnv(t)
	called := false
	h.WithOnRestore(func(string, string) error { called = true; return nil })
	name := createBackup(t, r)

	_ = database.Close() // VACUUM INTO cannot run on a closed database

	rec := postRestore(r, name)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), `"safety_snapshot_failed"`) {
		t.Fatalf("restore without a snapshot = %d %s, want 500 safety_snapshot_failed", rec.Code, rec.Body.String())
	}
	time.Sleep(700 * time.Millisecond)
	if called {
		t.Fatal("the restore ran without a safety snapshot")
	}
	if h.restoring.Load() {
		t.Fatal("the refused restore kept the restore lock")
	}
}

func TestASecondRestoreWhileOneRunsIsRefused(t *testing.T) {
	h, r, _, _ := restoreEnv(t)
	release := make(chan struct{})
	h.WithOnRestore(func(string, string) error { <-release; return nil })
	name := createBackup(t, r)

	if rec := postRestore(r, name); rec.Code != http.StatusOK {
		t.Fatalf("first restore = %d %s", rec.Code, rec.Body.String())
	}
	rec := postRestore(r, name)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"restore_in_progress"`) {
		t.Fatalf("second restore = %d %s, want 409 restore_in_progress", rec.Code, rec.Body.String())
	}
	close(release)
	waitIdle(t, h)
	if rec := postRestore(r, name); rec.Code != http.StatusOK {
		t.Fatalf("restore after the first finished = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

func rowsIn(t *testing.T, path string) []string {
	t.Helper()
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var names []string
	if err := d.Select(&names, `SELECT name FROM test_data ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	return names
}

// A successful restore puts the candidate in place, records "succeeded" for
// the next start, and exits 0.
func TestPerformRestoreSuccess(t *testing.T) {
	h, _, database, cfg := restoreEnv(t)
	candidate := filepath.Join(t.TempDir(), "candidate.db")
	if err := db.Snapshot(database, candidate); err != nil {
		t.Fatal(err)
	}
	database.MustExec(`INSERT INTO test_data (name) VALUES ('written-after-backup')`)
	safety := filepath.Join(t.TempDir(), "safety.db")
	if err := db.Snapshot(database, safety); err != nil {
		t.Fatal(err)
	}
	m := &RestoreMarker{ID: "r1", State: RestorePending, Source: "candidate.db"}

	if code := h.performRestore(candidate, safety, m); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if got := rowsIn(t, cfg.DBPath); len(got) != 1 || got[0] != "item-1" {
		t.Fatalf("database holds %v, want the candidate's rows only", got)
	}
	saved, _ := ReadRestoreMarker(cfg.DBPath)
	if saved == nil || saved.State != RestoreSucceeded || saved.FinishedAt == nil {
		t.Fatalf("marker = %+v, want succeeded", saved)
	}
}

// A failed restore used to leave the process running on a closed database
// while the console reported success. Now it rolls back, records the failure,
// and exits 1 so the service manager restarts it on the rolled-back file.
func TestPerformRestoreFailureRollsBackAndExitsNonZero(t *testing.T) {
	h, _, database, cfg := restoreEnv(t)
	database.MustExec(`INSERT INTO test_data (name) VALUES ('live-row')`)
	safety := filepath.Join(t.TempDir(), "safety.db")
	if err := db.Snapshot(database, safety); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "vanished.db") // removed after validation
	m := &RestoreMarker{ID: "r2", State: RestorePending, Source: "vanished.db"}

	if code := h.performRestore(missing, safety, m); code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	if got := rowsIn(t, cfg.DBPath); len(got) != 2 || got[1] != "live-row" {
		t.Fatalf("database holds %v after the rollback, want the live rows", got)
	}
	saved, _ := ReadRestoreMarker(cfg.DBPath)
	if saved == nil || saved.State != RestoreFailed || !saved.RolledBack || saved.ErrorCode != ErrCodeRestoreFailed {
		t.Fatalf("marker = %+v, want failed, rolled back, restore_failed", saved)
	}
}

// The outcome is written to the audit trail of the database that is live
// after the restart, exactly once; a restore the process died in is recorded
// as an interrupted failure.
func TestFinalizeRestoreMarkerAuditsOnce(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "x.db")

	if err := writeMarker(dbPath, &RestoreMarker{ID: "a", State: RestorePending, ActorID: "admin-1", Source: "b.db"}); err != nil {
		t.Fatal(err)
	}
	a := &recordingAudit{}
	m, err := FinalizeRestoreMarker(context.Background(), dbPath, a)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != RestoreFailed || m.ErrorCode != ErrCodeInterrupted || !m.Audited {
		t.Fatalf("pending marker finalized as %+v, want failed/interrupted and audited", m)
	}
	if a.count("system.backup_restore_failed") != 1 || !strings.HasPrefix(a.actions[0], "admin-1:") {
		t.Fatalf("audit = %v, want one backup_restore_failed by the operator who asked", a.actions)
	}
	if _, err := FinalizeRestoreMarker(context.Background(), dbPath, a); err != nil {
		t.Fatal(err)
	}
	if len(a.actions) != 1 {
		t.Fatalf("a second start audited again: %v", a.actions)
	}

	if err := writeMarker(dbPath, &RestoreMarker{ID: "c", State: RestoreSucceeded, ActorID: "admin-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := FinalizeRestoreMarker(context.Background(), dbPath, a); err != nil {
		t.Fatal(err)
	}
	if a.count("system.backup_restore_completed") != 1 {
		t.Fatalf("a successful restore was not audited as completed: %v", a.actions)
	}
}

// The console polls by restore id without a session (the session lived in
// the replaced database). Only the matching id gets an answer.
func TestRestoreStatusByIDNeedsTheID(t *testing.T) {
	database, dbPath, backupDir := setupTestDB(t)
	_ = database.Close()
	cfg := config.Config{DBPath: dbPath, BackupDir: backupDir, DBDriver: "sqlite"}
	h := NewHandler(cfg, nil, &mockAuditor{}, viewerAuthMw)
	r := chi.NewRouter()
	h.Register(r)
	id := newRestoreID()
	if err := writeMarker(dbPath, &RestoreMarker{ID: id, State: RestoreFailed, ErrorCode: ErrCodeRestoreFailed, RolledBack: true}); err != nil {
		t.Fatal(err)
	}

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	rec := get("/api/system/backup/restore-status/" + id)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"failed"`) || !strings.Contains(rec.Body.String(), `"rolled_back":true`) {
		t.Fatalf("status by id = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "safety_backup") || strings.Contains(rec.Body.String(), "actor_id") {
		t.Fatalf("the unauthenticated answer carries more than the outcome: %s", rec.Body.String())
	}
	if rec := get("/api/system/backup/restore-status/" + strings.Repeat("0", len(id))); rec.Code != http.StatusNotFound {
		t.Fatalf("wrong id = %d, want 404", rec.Code)
	}
	// The full record is for admins: this router's caller is a viewer.
	if rec := get("/api/system/backup/restore-status"); rec.Code != http.StatusForbidden {
		t.Fatalf("full status as viewer = %d, want 403", rec.Code)
	}
}

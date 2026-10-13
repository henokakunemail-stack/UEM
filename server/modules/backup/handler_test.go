package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type mockAuditor struct{}

func (m *mockAuditor) Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error {
	return nil
}

func setupTestDB(t *testing.T) (*sqlx.DB, string, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	backupDir := filepath.Join(dir, "backups")

	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	_, err = d.Exec(`CREATE TABLE IF NOT EXISTS test_data (id INTEGER PRIMARY KEY, name TEXT);
		INSERT INTO test_data (name) VALUES ('item-1');`)
	if err != nil {
		t.Fatalf("create test table: %v", err)
	}

	return d, dbPath, backupDir
}

func adminAuthMw(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := rbac.WithRole(r.Context(), rbac.RoleAdmin)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func viewerAuthMw(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := rbac.WithRole(r.Context(), rbac.RoleViewer)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestBackup_ListAndCreate(t *testing.T) {
	database, dbPath, backupDir := setupTestDB(t)
	defer database.Close()

	cfg := config.Config{
		DBPath:    dbPath,
		BackupDir: backupDir,
		DBDriver:  "sqlite",
	}

	h := NewHandler(cfg, database, &mockAuditor{}, adminAuthMw)
	r := chi.NewRouter()
	h.Register(r)

	// List initially empty
	req := httptest.NewRequest("GET", "/api/system/backups", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var list []BackupFileDTO
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 backups, got %d", len(list))
	}

	// Create backup
	req = httptest.NewRequest("POST", "/api/system/backup/create", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create backup expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var created BackupFileDTO
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Filename == "" || created.Size <= 0 {
		t.Fatalf("unexpected created backup DTO: %+v", created)
	}

	// List again - should have 1 item
	req = httptest.NewRequest("GET", "/api/system/backups", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	list = nil
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(list))
	}
	if list[0].Filename != created.Filename {
		t.Errorf("expected filename %s, got %s", created.Filename, list[0].Filename)
	}
}

func TestBackup_DownloadAndDelete(t *testing.T) {
	database, dbPath, backupDir := setupTestDB(t)
	defer database.Close()

	cfg := config.Config{
		DBPath:    dbPath,
		BackupDir: backupDir,
		DBDriver:  "sqlite",
	}

	h := NewHandler(cfg, database, &mockAuditor{}, adminAuthMw)
	r := chi.NewRouter()
	h.Register(r)

	// Create backup
	req := httptest.NewRequest("POST", "/api/system/backup/create", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var created BackupFileDTO
	_ = json.NewDecoder(rec.Body).Decode(&created)

	// Download backup
	req = httptest.NewRequest("GET", "/api/system/backup/download/"+created.Filename, nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("download expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Disposition") == "" {
		t.Error("missing Content-Disposition header")
	}
	if rec.Body.Len() == 0 {
		t.Error("empty downloaded body")
	}

	// Path traversal test
	req = httptest.NewRequest("GET", "/api/system/backup/download/..%2F..%2Fsecret.db", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
		t.Errorf("expected rejection for path traversal, got %d", rec.Code)
	}

	// Delete backup
	req = httptest.NewRequest("DELETE", "/api/system/backup/"+created.Filename, nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("delete expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify deleted
	req = httptest.NewRequest("GET", "/api/system/backup/download/"+created.Filename, nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for deleted file, got %d", rec.Code)
	}
}

func TestBackup_Restore(t *testing.T) {
	database, dbPath, backupDir := setupTestDB(t)
	defer database.Close()

	cfg := config.Config{
		DBPath:    dbPath,
		BackupDir: backupDir,
		DBDriver:  "sqlite",
	}

	restoredCandidate := ""
	restoredSafety := ""
	restoreDone := make(chan struct{})

	h := NewHandler(cfg, database, &mockAuditor{}, adminAuthMw).
		WithOnRestore(func(candidate, safety string) error {
			restoredCandidate = candidate
			restoredSafety = safety
			close(restoreDone)
			return nil
		})
	r := chi.NewRouter()
	h.Register(r)

	// Create a valid backup
	req := httptest.NewRequest("POST", "/api/system/backup/create", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var created BackupFileDTO
	_ = json.NewDecoder(rec.Body).Decode(&created)

	// Restore valid backup
	req = httptest.NewRequest("POST", "/api/system/backup/restore/"+created.Filename, nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("restore expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp RestoreResponseDTO
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Status != "restoring" || resp.SafetyBackup == "" {
		t.Errorf("unexpected restore response: %+v", resp)
	}

	<-restoreDone
	if filepath.Base(restoredCandidate) != created.Filename {
		t.Errorf("expected restored candidate %s, got %s", created.Filename, restoredCandidate)
	}
	if restoredSafety == "" {
		t.Errorf("expected safety backup to be generated")
	}

	// Attempt restore of a corrupt file
	corruptPath := filepath.Join(backupDir, "corrupt.db")
	_ = os.WriteFile(corruptPath, []byte("NOT A SQLITE FILE"), 0o600)

	req = httptest.NewRequest("POST", "/api/system/backup/restore/corrupt.db", nil)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for corrupt file restore, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBackup_UploadRestore(t *testing.T) {
	database, dbPath, backupDir := setupTestDB(t)
	defer database.Close()

	cfg := config.Config{
		DBPath:    dbPath,
		BackupDir: backupDir,
		DBDriver:  "sqlite",
	}

	restoredCandidate := ""
	restoreDone := make(chan struct{})

	h := NewHandler(cfg, database, &mockAuditor{}, adminAuthMw).
		WithOnRestore(func(candidate, safety string) error {
			restoredCandidate = candidate
			close(restoreDone)
			return nil
		})
	r := chi.NewRouter()
	h.Register(r)

	// Take snapshot to use as upload source
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	if err := db.Snapshot(database, sourcePath); err != nil {
		t.Fatalf("snapshot source: %v", err)
	}
	data, _ := os.ReadFile(sourcePath)

	// Build multipart request
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "uploaded-source.db")
	_, _ = io.Copy(part, bytes.NewReader(data))
	_ = writer.Close()

	req := httptest.NewRequest("POST", "/api/system/backup/upload-restore", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload-restore expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	<-restoreDone
	if restoredCandidate == "" {
		t.Errorf("expected candidate to be restored")
	}
}

func TestBackup_RBAC(t *testing.T) {
	database, dbPath, backupDir := setupTestDB(t)
	defer database.Close()

	cfg := config.Config{
		DBPath:    dbPath,
		BackupDir: backupDir,
		DBDriver:  "sqlite",
	}

	// Handler with RoleViewer auth middleware
	h := NewHandler(cfg, database, &mockAuditor{}, viewerAuthMw)
	r := chi.NewRouter()
	h.Register(r)

	req := httptest.NewRequest("GET", "/api/system/backups", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for viewer, got %d", rec.Code)
	}
}

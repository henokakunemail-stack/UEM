package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type Auditor interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

type BackupFileDTO struct {
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"created_at"`
	IsAuto    bool   `json:"is_auto"`
}

type RestoreResponseDTO struct {
	Status       string `json:"status"`
	Message      string `json:"message"`
	SafetyBackup string `json:"safety_backup,omitempty"`
}

type Handler struct {
	cfg       config.Config
	database  *sqlx.DB
	auditor   Auditor
	authMw    func(http.Handler) http.Handler
	onRestore func(candidate, safety string) error
}

func NewHandler(cfg config.Config, database *sqlx.DB, auditor Auditor, authMw func(http.Handler) http.Handler) *Handler {
	return &Handler{
		cfg:      cfg,
		database: database,
		auditor:  auditor,
		authMw:   authMw,
	}
}

// WithOnRestore sets a test or custom restore hook instead of process restart.
func (h *Handler) WithOnRestore(fn func(candidate, safety string) error) *Handler {
	h.onRestore = fn
	return h
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(cr chi.Router) {
		cr.Use(h.authMw)
		cr.Use(rbac.RequireRole(rbac.RoleAdmin))

		cr.Get("/api/system/backups", h.handleList)
		cr.Post("/api/system/backup/create", h.handleCreate)
		cr.Get("/api/system/backup/download/{filename}", h.handleDownload)
		cr.Get("/api/system/backup/download-active", h.handleDownloadActive)
		cr.Delete("/api/system/backup/{filename}", h.handleDelete)
		cr.Post("/api/system/backup/restore/{filename}", h.handleRestore)
		cr.Post("/api/system/backup/upload-restore", h.handleUploadRestore)
	})
}

func (h *Handler) isSQLite() bool {
	driver := strings.ToLower(h.cfg.DBDriver)
	if driver == "postgres" || driver == "postgresql" {
		return false
	}
	if strings.HasPrefix(h.cfg.DBURL, "postgres://") || strings.HasPrefix(h.cfg.DBURL, "postgresql://") {
		return false
	}
	return true
}

func (h *Handler) safeBackupPath(filename string) (string, error) {
	if filename == "" || strings.ContainsAny(filename, `/\`) {
		return "", errors.New("invalid backup filename")
	}
	if filepath.Base(filename) != filename || filename == "." || filename == ".." {
		return "", errors.New("path traversal detected")
	}
	if filepath.Ext(filename) != ".db" {
		return "", errors.New("backup file must have .db extension")
	}

	backupDir := h.cfg.BackupDir
	if backupDir == "" {
		backupDir = filepath.Join(filepath.Dir(h.cfg.DBPath), "backups")
	}

	target := filepath.Clean(filepath.Join(backupDir, filename))
	rel, err := filepath.Rel(backupDir, target)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return "", errors.New("target escapes backup directory")
	}

	// Never allow operations directly targeting the active database file
	activeClean := filepath.Clean(h.cfg.DBPath)
	if target == activeClean {
		return "", errors.New("cannot operate directly on active database path")
	}

	return target, nil
}

func (h *Handler) ensureBackupDir() error {
	dir := h.cfg.BackupDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(h.cfg.DBPath), "backups")
	}
	return os.MkdirAll(dir, 0o750)
}

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	if !h.isSQLite() {
		writeErr(w, http.StatusBadRequest, errors.New("backup is supported only on SQLite"))
		return
	}

	dir := h.cfg.BackupDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(h.cfg.DBPath), "backups")
	}

	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("read backup dir: %w", err))
		return
	}

	results := make([]BackupFileDTO, 0)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".db" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}

		isAuto := !strings.Contains(name, "manual") &&
			!strings.Contains(name, "uploaded") &&
			!strings.Contains(name, "pre-restore")

		results = append(results, BackupFileDTO{
			Filename:  name,
			Size:      info.Size(),
			CreatedAt: info.ModTime().UTC().Format(time.RFC3339),
			IsAuto:    isAuto,
		})
	}

	// Sort newest first
	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt > results[j].CreatedAt
	})

	writeJSON(w, http.StatusOK, results)
}

func (h *Handler) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !h.isSQLite() {
		writeErr(w, http.StatusBadRequest, errors.New("backup is supported only on SQLite"))
		return
	}

	if err := h.ensureBackupDir(); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("create backup dir: %w", err))
		return
	}

	dir := h.cfg.BackupDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(h.cfg.DBPath), "backups")
	}

	filename := fmt.Sprintf("endpoint-mgmt-manual-%s.db", time.Now().UTC().Format("20060102T150405Z"))
	destPath := filepath.Join(dir, filename)

	if err := db.Snapshot(h.database, destPath); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("create snapshot: %w", err))
		return
	}

	info, err := os.Stat(destPath)
	size := int64(0)
	if err == nil {
		size = info.Size()
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "system.backup_create", filename, map[string]string{
		"filename": filename,
		"size":     fmt.Sprintf("%d", size),
	})

	writeJSON(w, http.StatusOK, BackupFileDTO{
		Filename:  filename,
		Size:      size,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		IsAuto:    false,
	})
}

func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	if !h.isSQLite() {
		writeErr(w, http.StatusBadRequest, errors.New("backup is supported only on SQLite"))
		return
	}

	filename := chi.URLParam(r, "filename")
	target, err := h.safeBackupPath(filename)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, errors.New("backup file not found"))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	f, err := os.Open(target)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("open backup file: %w", err))
		return
	}
	defer f.Close()

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "system.backup_download", filename, map[string]string{
		"filename": filename,
	})

	w.Header().Set("Content-Type", "application/x-sqlite3")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	_, _ = io.Copy(w, f)
}

func (h *Handler) handleDownloadActive(w http.ResponseWriter, r *http.Request) {
	if !h.isSQLite() {
		writeErr(w, http.StatusBadRequest, errors.New("backup is supported only on SQLite"))
		return
	}

	if err := h.ensureBackupDir(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	dir := h.cfg.BackupDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(h.cfg.DBPath), "backups")
	}

	filename := fmt.Sprintf("endpoint-mgmt-live-%s.db", time.Now().UTC().Format("20060102T150405Z"))
	tmpPath := filepath.Join(dir, filename)
	defer os.Remove(tmpPath)

	if err := db.Snapshot(h.database, tmpPath); err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("live snapshot failed: %w", err))
		return
	}

	info, err := os.Stat(tmpPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "system.backup_download_live", filename, nil)

	w.Header().Set("Content-Type", "application/x-sqlite3")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	_, _ = io.Copy(w, f)
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !h.isSQLite() {
		writeErr(w, http.StatusBadRequest, errors.New("backup is supported only on SQLite"))
		return
	}

	filename := chi.URLParam(r, "filename")
	target, err := h.safeBackupPath(filename)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, errors.New("backup file not found"))
			return
		}
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("delete backup: %w", err))
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "system.backup_delete", filename, map[string]string{
		"filename": filename,
	})

	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "deleted",
		"filename": filename,
	})
}

func (h *Handler) handleRestore(w http.ResponseWriter, r *http.Request) {
	if !h.isSQLite() {
		writeErr(w, http.StatusBadRequest, errors.New("restore is supported only on SQLite"))
		return
	}

	filename := chi.URLParam(r, "filename")
	target, err := h.safeBackupPath(filename)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, errors.New("backup file not found"))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// 1. Verify candidate database integrity
	if err := db.QuickCheck(target); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("backup candidate integrity check failed: %w", err))
		return
	}

	// 2. Take automatic pre-restore safety snapshot
	dir := h.cfg.BackupDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(h.cfg.DBPath), "backups")
	}
	safetyName := fmt.Sprintf("endpoint-mgmt-pre-restore-%s.db", time.Now().UTC().Format("20060102T150405Z"))
	safetyPath := filepath.Join(dir, safetyName)
	if err := db.Snapshot(h.database, safetyPath); err != nil {
		log.Warn().Err(err).Msg("failed to take pre-restore safety snapshot")
	}

	// 3. Log audit action
	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "system.backup_restore", filename, map[string]string{
		"source":        filename,
		"safety_backup": safetyName,
	})

	// 4. Return success response
	writeJSON(w, http.StatusOK, RestoreResponseDTO{
		Status:       "restoring",
		Message:      "Database validated. System restore initiated. Reconnecting...",
		SafetyBackup: safetyName,
	})

	// 5. Trigger asynchronous restore worker
	go h.triggerRestore(target, safetyPath)
}

func (h *Handler) handleUploadRestore(w http.ResponseWriter, r *http.Request) {
	if !h.isSQLite() {
		writeErr(w, http.StatusBadRequest, errors.New("restore is supported only on SQLite"))
		return
	}

	// Cap multipart form to 500 MiB
	if err := r.ParseMultipartForm(500 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid multipart form or file too large (max 500MB): %w", err))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("file field is required"))
		return
	}
	defer file.Close()

	if err := h.ensureBackupDir(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	dir := h.cfg.BackupDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(h.cfg.DBPath), "backups")
	}

	uploadedName := fmt.Sprintf("endpoint-mgmt-uploaded-%s.db", time.Now().UTC().Format("20060102T150405Z"))
	uploadedPath := filepath.Join(dir, uploadedName)

	dst, err := os.OpenFile(uploadedPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("create destination file: %w", err))
		return
	}

	if _, err := io.Copy(dst, file); err != nil {
		dst.Close()
		_ = os.Remove(uploadedPath)
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("save uploaded file: %w", err))
		return
	}
	_ = dst.Close()

	// Validate integrity
	if err := db.QuickCheck(uploadedPath); err != nil {
		_ = os.Remove(uploadedPath)
		writeErr(w, http.StatusBadRequest, fmt.Errorf("uploaded file is not a valid SQLite database: %w", err))
		return
	}

	// Pre-restore safety snapshot
	safetyName := fmt.Sprintf("endpoint-mgmt-pre-restore-%s.db", time.Now().UTC().Format("20060102T150405Z"))
	safetyPath := filepath.Join(dir, safetyName)
	if err := db.Snapshot(h.database, safetyPath); err != nil {
		log.Warn().Err(err).Msg("failed to take pre-restore safety snapshot")
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "system.backup_restore_upload", header.Filename, map[string]string{
		"original_filename": header.Filename,
		"stored_filename":   uploadedName,
		"safety_backup":     safetyName,
	})

	writeJSON(w, http.StatusOK, RestoreResponseDTO{
		Status:       "restoring",
		Message:      "Uploaded database validated. System restore initiated. Reconnecting...",
		SafetyBackup: safetyName,
	})

	go h.triggerRestore(uploadedPath, safetyPath)
}

func (h *Handler) triggerRestore(candidatePath, safetyPath string) {
	// Give HTTP response 500ms to completely write and flush to client
	time.Sleep(500 * time.Millisecond)

	if h.onRestore != nil {
		if err := h.onRestore(candidatePath, safetyPath); err != nil {
			log.Error().Err(err).Msg("onRestore hook failed")
		}
		return
	}

	log.Warn().Str("candidate", candidatePath).Msg("initiating database restore")
	_ = h.database.Close()

	if err := db.Restore(candidatePath, h.cfg.DBPath); err != nil {
		log.Error().Err(err).Msg("database restore failed, attempting rollback to safety backup")
		if safetyPath != "" {
			_ = db.Restore(safetyPath, h.cfg.DBPath)
		}
		return
	}

	log.Info().Msg("database restored successfully; restarting server process")
	os.Exit(0)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

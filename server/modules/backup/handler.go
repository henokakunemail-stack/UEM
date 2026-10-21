package backup

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
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
	// RestoreID identifies this restore. The console polls its outcome by it
	// after the restart, when its own session may no longer exist.
	RestoreID string `json:"restore_id,omitempty"`
}

type Handler struct {
	cfg       config.Config
	database  *sqlx.DB
	auditor   Auditor
	authMw    func(http.Handler) http.Handler
	onRestore func(candidate, safety string) error
	// restoring is held from the request until the process exits, so a second
	// restore cannot start while one runs.
	restoring atomic.Bool
	// exit ends the process after a restore; a field so tests can see the code.
	exit func(code int)
}

func NewHandler(cfg config.Config, database *sqlx.DB, auditor Auditor, authMw func(http.Handler) http.Handler) *Handler {
	return &Handler{
		cfg:      cfg,
		database: database,
		auditor:  auditor,
		authMw:   authMw,
		exit:     os.Exit,
	}
}

// WithOnRestore sets a test or custom restore hook instead of process restart.
func (h *Handler) WithOnRestore(fn func(candidate, safety string) error) *Handler {
	h.onRestore = fn
	return h
}

func (h *Handler) Register(r chi.Router) {
	// Outside the auth group on purpose: after a restore the operator's
	// session is in the database that was replaced. The 128-bit id from the
	// restore response is the credential, and the answer is only the outcome.
	r.Get("/api/system/backup/restore-status/{id}", h.handleRestoreStatusByID)

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
		cr.Get("/api/system/backup/restore-status", h.handleRestoreStatus)
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
	if !h.restoring.CompareAndSwap(false, true) {
		writeErrCode(w, http.StatusConflict, "restore_in_progress", "a restore is already running")
		return
	}
	handedOff := false
	defer func() {
		if !handedOff {
			h.restoring.Store(false)
		}
	}()

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

	if err := db.QuickCheck(target); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("backup candidate integrity check failed: %w", err))
		return
	}

	handedOff = h.beginRestore(w, r, target, filename, "system.backup_restore", map[string]string{"source": filename})
}

func (h *Handler) handleUploadRestore(w http.ResponseWriter, r *http.Request) {
	if !h.isSQLite() {
		writeErr(w, http.StatusBadRequest, errors.New("restore is supported only on SQLite"))
		return
	}
	// Taken before the upload is read, so a second upload is refused before
	// half a gigabyte is spooled to disk.
	if !h.restoring.CompareAndSwap(false, true) {
		writeErrCode(w, http.StatusConflict, "restore_in_progress", "a restore is already running")
		return
	}
	handedOff := false
	defer func() {
		if !handedOff {
			h.restoring.Store(false)
		}
	}()

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

	uploadedName := fmt.Sprintf("endpoint-mgmt-uploaded-%s.db", time.Now().UTC().Format("20060102T150405Z"))
	uploadedPath := filepath.Join(h.backupDir(), uploadedName)

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

	handedOff = h.beginRestore(w, r, uploadedPath, uploadedName, "system.backup_restore_upload", map[string]string{
		"original_filename": header.Filename,
		"stored_filename":   uploadedName,
	})
}

func (h *Handler) backupDir() string {
	if h.cfg.BackupDir != "" {
		return h.cfg.BackupDir
	}
	return filepath.Join(filepath.Dir(h.cfg.DBPath), "backups")
}

// beginRestore is the part both restore routes share once the candidate is
// valid. It reports whether the restore was handed to the worker (which then
// owns the restoring flag until the process exits).
//
// The order is deliberate:
//  1. The request is audited before the safety snapshot, so the entry is
//     inside the snapshot and survives a rollback. Written after it, as
//     before, it existed only in the database the restore threw away.
//  2. A restore without a safety snapshot is not started: there would be
//     nothing to roll back to. It used to log a warning and go ahead.
//  3. The pending marker is written before the response, so a crash at any
//     later point is recorded as an interrupted restore at the next start.
func (h *Handler) beginRestore(w http.ResponseWriter, r *http.Request, candidate, source, action string, details map[string]string) bool {
	actorID := auth.UserIDFromContext(r.Context())
	safetyName := fmt.Sprintf("endpoint-mgmt-pre-restore-%s.db", time.Now().UTC().Format("20060102T150405Z"))
	safetyPath := filepath.Join(h.backupDir(), safetyName)

	details["safety_backup"] = safetyName
	_ = h.auditor.Log(r.Context(), "user", actorID, action, source, details)

	if err := db.Snapshot(h.database, safetyPath); err != nil {
		log.Error().Err(err).Msg("restore refused: pre-restore safety snapshot failed")
		_ = h.auditor.Log(r.Context(), "user", actorID, "system.backup_restore_failed", source, map[string]string{
			"source": source, "reason": "safety_snapshot_failed", "error": err.Error(),
		})
		writeErrCode(w, http.StatusInternalServerError, "safety_snapshot_failed",
			"the safety snapshot taken before every restore failed, so the restore was not started: "+err.Error())
		return false
	}

	m := &RestoreMarker{
		ID:           newRestoreID(),
		State:        RestorePending,
		Source:       source,
		SafetyBackup: safetyName,
		ActorID:      actorID,
		RequestedAt:  time.Now().UTC(),
	}
	if err := writeMarker(h.cfg.DBPath, m); err != nil {
		writeErrCode(w, http.StatusInternalServerError, "restore_marker_failed",
			"could not record the restore, so it was not started: "+err.Error())
		return false
	}

	writeJSON(w, http.StatusOK, RestoreResponseDTO{
		Status:       "restoring",
		Message:      "Database validated. System restore initiated. Reconnecting...",
		SafetyBackup: safetyName,
		RestoreID:    m.ID,
	})
	go h.triggerRestore(candidate, safetyPath, m)
	return true
}

func (h *Handler) triggerRestore(candidatePath, safetyPath string, m *RestoreMarker) {
	// Give HTTP response 500ms to completely write and flush to client
	time.Sleep(500 * time.Millisecond)

	if h.onRestore != nil {
		if err := h.onRestore(candidatePath, safetyPath); err != nil {
			log.Error().Err(err).Msg("onRestore hook failed")
		}
		h.restoring.Store(false)
		return
	}

	code := h.performRestore(candidatePath, safetyPath, m)
	log.Warn().Int("exit_code", code).Msg("restore finished; the server exits now and is restarted by its " +
		"service manager (systemd, Docker, Windows service). Run by hand, start it again.")
	h.exit(code)
}

// performRestore replaces the database and records the outcome, returning the
// exit code. It never leaves the process running: the database handle every
// module shares is closed here, and a server that kept serving after a failed
// restore -- as it used to -- answered every request with "database is
// closed" until someone restarted it by hand, while the console reported
// success. Exiting 1 hands the restart to the service manager, which reopens
// whichever database is in place: the restored one, or the rolled-back one.
func (h *Handler) performRestore(candidatePath, safetyPath string, m *RestoreMarker) int {
	log.Warn().Str("candidate", candidatePath).Msg("initiating database restore")
	// Fold the WAL into the main file first, so the copy the safety snapshot
	// does not cover is not lost when Restore removes the -wal sidecar.
	_, _ = h.database.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	_ = h.database.Close()

	err := db.Restore(candidatePath, h.cfg.DBPath)
	now := time.Now().UTC()
	m.FinishedAt = &now
	if err == nil {
		m.State = RestoreSucceeded
		if werr := writeMarker(h.cfg.DBPath, m); werr != nil {
			log.Error().Err(werr).Msg("restore succeeded but its marker could not be written")
		}
		log.Info().Msg("database restored successfully")
		return 0
	}

	log.Error().Err(err).Msg("database restore failed, rolling back to the safety snapshot")
	m.State, m.Error = RestoreFailed, err.Error()
	if rbErr := db.Restore(safetyPath, h.cfg.DBPath); rbErr != nil {
		m.ErrorCode = ErrCodeRollbackFailed
		m.Error += "; rollback to " + filepath.Base(safetyPath) + " also failed: " + rbErr.Error()
		log.Error().Err(rbErr).Msg("rollback to the safety snapshot failed")
	} else {
		m.ErrorCode, m.RolledBack = ErrCodeRestoreFailed, true
	}
	if werr := writeMarker(h.cfg.DBPath, m); werr != nil {
		log.Error().Err(werr).Msg("restore failed and its marker could not be written")
	}
	return 1
}

// handleRestoreStatus shows the last restore to an admin.
func (h *Handler) handleRestoreStatus(w http.ResponseWriter, r *http.Request) {
	m, err := ReadRestoreMarker(h.cfg.DBPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if m == nil {
		writeJSON(w, http.StatusOK, map[string]string{"state": "none"})
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleRestoreStatusByID answers the console waiting on a restore it asked
// for. Without a session, so only for the matching id and only the outcome.
func (h *Handler) handleRestoreStatusByID(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	m, err := ReadRestoreMarker(h.cfg.DBPath)
	if err != nil || m == nil || len(id) != len(m.ID) ||
		subtle.ConstantTimeCompare([]byte(id), []byte(m.ID)) != 1 {
		writeErr(w, http.StatusNotFound, errors.New("restore not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          m.ID,
		"state":       m.State,
		"finished_at": m.FinishedAt,
		"error_code":  m.ErrorCode,
		"rolled_back": m.RolledBack,
	})
}

func writeErrCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

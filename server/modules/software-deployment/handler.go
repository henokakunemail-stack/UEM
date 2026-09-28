package softwaredeployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

type HubDispatcher interface {
	Online(deviceID string) bool
	SendTo(deviceID string, payload []byte) bool
}

// validPackageTypes mirrors the type choices the console offers per OS. The
// agent builds a different command for each (msiexec /i, dpkg -i, ...), so a
// type from the wrong OS would produce an unrunnable command on the endpoint.
var validPackageTypes = map[string]map[string]bool{
	OSTargetWindows: {PkgTypeMSI: true, PkgTypeEXE: true, PkgTypeScript: true},
	OSTargetLinux:   {"deb": true, "rpm": true, PkgTypeScript: true},
	OSTargetMacOS:   {"pkg": true, PkgTypeScript: true},
}

// typeExtensions lists the file suffixes that legitimately carry each type.
// An empty list means the type is chosen by content, not by name: a
// PowerShell or shell script is routinely named .ps1/.sh but is just as often
// uploaded with no usable suffix, so those are not policed on extension.
var typeExtensions = map[string][]string{
	PkgTypeMSI: {".msi", ".msp"},
	PkgTypeEXE: {".exe"},
	"deb":     {".deb"},
	"rpm":     {".rpm"},
	"pkg":     {".pkg", ".mpkg"},
}

// checkExtensionMatches verifies a declared package type against the uploaded
// file name. It returns a human-readable reason so the operator can correct the
// type in the form instead of guessing from a failed task.
func checkExtensionMatches(osTarget, pkgType, fileName string) (string, bool) {
	exts, policed := typeExtensions[pkgType]
	if !policed {
		return "", true
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	for _, ok := range exts {
		if ext == ok {
			return "", true
		}
	}
	return fmt.Sprintf(
		"package_type %q does not match the file name %q (expected one of %s). "+
			"Uploading a %s as %q makes the agent run a command that cannot open it.",
		pkgType, filepath.Base(fileName), strings.Join(exts, ", "),
		filepath.Base(fileName), pkgType), false
}


type AuditLogger interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

type Handler struct {
	repo       *Repository
	hub        HubDispatcher
	audit      AuditLogger
	storageDir string
	authMW     func(http.Handler) http.Handler
	// devices authenticates agent-facing endpoints by the per-device secret.
	// Agent endpoints have no user JWT, so without this any client that can
	// reach the port could report progress for arbitrary tasks or pull packages.
	devices devicemgmt.SecretLookup
}

func NewHandler(repo *Repository, hub HubDispatcher, auditLogger AuditLogger, storageDir string, authMW func(http.Handler) http.Handler, devices devicemgmt.SecretLookup) *Handler {
	if storageDir == "" {
		storageDir = "./data/packages"
	}
	_ = os.MkdirAll(storageDir, 0o755)
	return &Handler{
		repo:       repo,
		hub:        hub,
		audit:      auditLogger,
		storageDir: storageDir,
		authMW:     authMW,
		devices:    devices,
	}
}

func (h *Handler) Register(r chi.Router) {
	// Operator Console API (Protected by JWT + RBAC)
	r.Group(func(r chi.Router) {
		r.Use(h.authMW)

		// Packages
		r.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/software/packages", h.listPackages)
		r.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/software/packages/{id}", h.getPackage)
		r.With(rbac.RequireRole(rbac.RoleTechnician)).Post("/api/software/packages", h.uploadPackage)
		r.With(rbac.RequireRole(rbac.RoleAdmin)).Delete("/api/software/packages/{id}", h.deletePackage)

		// Deployments
		r.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/software/deployments", h.listDeployments)
		r.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/software/deployments/{id}", h.getDeployment)
		r.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/software/deployments/{id}/tasks", h.listTasks)
		r.With(rbac.RequireRole(rbac.RoleTechnician)).Post("/api/software/deployments", h.createDeployment)
	})

	// Agent Endpoints (Authenticated via device header or unauthenticated download token)
	r.Get("/api/agent/packages/{id}/download", h.downloadPackage)
	r.Post("/api/agent/tasks/{id}/progress", h.reportProgress)
}

func (h *Handler) uploadPackage(w http.ResponseWriter, r *http.Request) {
	// 500 MB max package size
	if err := r.ParseMultipartForm(500 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "file too large or invalid multipart form")
		return
	}

	name := r.FormValue("name")
	version := r.FormValue("version")
	osTarget := r.FormValue("os_target")
	pkgType := r.FormValue("package_type")
	installArgs := r.FormValue("install_args")
	uninstallArgs := r.FormValue("uninstall_args")

	if name == "" || version == "" || osTarget == "" || pkgType == "" {
		writeErr(w, http.StatusBadRequest, "name, version, os_target, and package_type are required")
		return
	}
	if !validPackageTypes[osTarget][pkgType] {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("package_type %q is not valid for os_target %q", pkgType, osTarget))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing package file: "+err.Error())
		return
	}
	defer file.Close()

	// The declared type decides which installer command the agent builds, and a
	// mismatch fails on the endpoint rather than here: a .exe SFX uploaded as
	// 'msi' reached msiexec, which exits 1620 ("could not be opened") and
	// leaves the operator with a failed task and no clue why. Catch it at
	// upload, where the file name and the type are both still in hand.
	if msg, ok := checkExtensionMatches(osTarget, pkgType, header.Filename); !ok {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	// Silent-by-construction. An .exe has no universal silent switch -- NSIS
	// wants /S, Inno Setup /VERYSILENT, a WinRAR SFX /s -- so a package that
	// carries none will open a setup dialog on the endpoint's desktop and block
	// on a human, stalling the task and interrupting the user at their desk.
	// Reject it at upload while the operator can still fix it.
	if pkgType == PkgTypeEXE && strings.TrimSpace(installArgs) == "" {
		writeErr(w, http.StatusBadRequest,
			"install_args is required for an .exe package: there is no universal silent flag "+
				"(NSIS uses /S, Inno Setup uses /VERYSILENT /SUPPRESSMSGBOXES /NORESTART, "+
				"a WinRAR SFX uses /s). Without them the installer opens a window on the endpoint.")
		return
	}

	pkgID := NewID()
	destName := fmt.Sprintf("%s_%s", pkgID, filepath.Base(header.Filename))
	destPath := filepath.Join(h.storageDir, destName)

	destFile, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create destination file: "+err.Error())
		return
	}
	defer destFile.Close()

	hasher := sha256.New()
	mw := io.MultiWriter(destFile, hasher)

	size, err := io.Copy(mw, file)
	if err != nil {
		_ = os.Remove(destPath)
		writeErr(w, http.StatusInternalServerError, "failed to save file: "+err.Error())
		return
	}

	sha256Hex := hex.EncodeToString(hasher.Sum(nil))
	now := time.Now().UTC()

	pkg := SoftwarePackage{
		ID:            pkgID,
		Name:          name,
		Version:       version,
		OSTarget:      osTarget,
		PackageType:   pkgType,
		FileName:      header.Filename,
		FileSize:      size,
		SHA256:        sha256Hex,
		StoragePath:   destPath,
		InstallArgs:   installArgs,
		UninstallArgs: uninstallArgs,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := h.repo.CreatePackage(r.Context(), pkg); err != nil {
		_ = os.Remove(destPath)
		writeErr(w, http.StatusInternalServerError, "failed to store package record: "+err.Error())
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	if h.audit != nil {
		_ = h.audit.Log(r.Context(), "user", actorID, "software.upload", pkg.ID, map[string]string{
			"package_name": pkg.Name, "version": pkg.Version, "sha256": sha256Hex,
		})
	}

	writeJSON(w, http.StatusCreated, pkg)
}

func (h *Handler) listPackages(w http.ResponseWriter, r *http.Request) {
	pkgs, err := h.repo.ListPackages(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pkgs)
}

func (h *Handler) getPackage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	pkg, err := h.repo.GetPackage(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "package not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pkg)
}

func (h *Handler) deletePackage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	pkg, err := h.repo.GetPackage(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "package not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := h.repo.DeletePackage(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = os.Remove(pkg.StoragePath)

	actorID := auth.UserIDFromContext(r.Context())
	if h.audit != nil {
		_ = h.audit.Log(r.Context(), "user", actorID, "software.delete", id, map[string]string{
			"package_name": pkg.Name,
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) createDeployment(w http.ResponseWriter, r *http.Request) {
	var req CreateDeploymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" || req.PackageID == "" || req.TargetType == "" {
		writeErr(w, http.StatusBadRequest, "name, package_id, and target_type are required")
		return
	}

	pkg, err := h.repo.GetPackage(r.Context(), req.PackageID)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "package not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	deviceIDs, err := h.repo.ResolveTargetDevices(r.Context(), req.TargetType, req.TargetID, pkg.OSTarget)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "target resolution failed: "+err.Error())
		return
	}

	if len(deviceIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "no matching active endpoints for target specification")
		return
	}

	depID := NewID()
	actorID := auth.UserIDFromContext(r.Context())
	now := time.Now().UTC()

	dep := SoftwareDeployment{
		ID:         depID,
		PackageID:  req.PackageID,
		Name:       req.Name,
		TargetType: req.TargetType,
		TargetID:   req.TargetID,
		CreatedBy:  actorID,
		Status:     "running",
		CreatedAt:  now,
	}

	tasks, err := h.repo.CreateDeploymentTx(r.Context(), dep, deviceIDs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create deployment: "+err.Error())
		return
	}

	// Dispatch tasks to online agents immediately via WebSocket command
	dispatchedCount := 0
	for _, task := range tasks {
		if h.hub != nil && h.hub.Online(task.DeviceID) {
			cmdPayload := map[string]any{
				"task_id":      task.ID,
				"package_id":   pkg.ID,
				"package_name": pkg.Name,
				"download_url": fmt.Sprintf("/api/agent/packages/%s/download", pkg.ID),
				"file_name":    pkg.FileName,
				"package_type": pkg.PackageType,
				"sha256":       pkg.SHA256,
				"install_args": pkg.InstallArgs,
			}
			env := transport.Envelope{
				Type:    transport.TypeCommand,
				ID:      NewID(),
				Command: "software.install",
				Payload: cmdPayload,
			}
			envBytes, _ := json.Marshal(env)

			if h.hub.SendTo(task.DeviceID, envBytes) {
				_ = h.repo.UpdateTaskProgress(r.Context(), TaskProgressReport{
					TaskID: task.ID,
					Status: TaskStatusDispatched,
				})
				dispatchedCount++
			}
		}
	}

	if h.audit != nil {
		_ = h.audit.Log(r.Context(), "user", actorID, "software.deploy", dep.ID, map[string]string{
			"deployment_name": dep.Name, "package_id": pkg.ID, "targets_total": strconv.Itoa(len(tasks)), "dispatched_now": strconv.Itoa(dispatchedCount),
		})
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"deployment":      dep,
		"tasks_total":     len(tasks),
		"dispatched_live": dispatchedCount,
	})
}

func (h *Handler) listDeployments(w http.ResponseWriter, r *http.Request) {
	deps, err := h.repo.ListDeployments(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, deps)
}

func (h *Handler) getDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dep, err := h.repo.GetDeployment(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "deployment not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dep)
}

func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	tasks, err := h.repo.ListDeploymentTasks(r.Context(), depID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (h *Handler) downloadPackage(w http.ResponseWriter, r *http.Request) {
	if _, ok := devicemgmt.AuthenticateAgent(w, r, h.devices); !ok {
		return
	}
	pkgID := chi.URLParam(r, "id")
	pkg, err := h.repo.GetPackage(r.Context(), pkgID)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	f, err := os.Open(pkg.StoragePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", pkg.FileName))
	w.Header().Set("X-Package-SHA256", pkg.SHA256)
	w.Header().Set("Content-Length", strconv.FormatInt(pkg.FileSize, 10))

	http.ServeContent(w, r, pkg.FileName, pkg.UpdatedAt, f)
}

func (h *Handler) reportProgress(w http.ResponseWriter, r *http.Request) {
	if _, ok := devicemgmt.AuthenticateAgent(w, r, h.devices); !ok {
		return
	}
	taskID := chi.URLParam(r, "id")
	var rep TaskProgressReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	rep.TaskID = taskID

	if err := h.repo.UpdateTaskProgress(r.Context(), rep); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "task not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("task_id", taskID).Str("status", rep.Status).Msg("agent reported software task progress")
	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

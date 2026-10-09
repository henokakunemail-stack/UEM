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
	"deb":      {".deb"},
	"rpm":      {".rpm"},
	"pkg":      {".pkg", ".mpkg"},
}

// checkExtensionMatches verifies a declared package type against the uploaded
// file name. It returns a human-readable reason so the operator can correct the
// type in the form instead of guessing from a failed task.
//
// There is no OS dimension to this check: typeExtensions is keyed by package
// type alone, because an extension like .msi or .deb is already unambiguous
// about its platform. osTarget used to be a parameter here and was never read,
// which made it look like the check was platform-aware when it was not.
func checkExtensionMatches(pkgType, fileName string) (string, bool) {
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
	//
	// It is the full Repository rather than SecretLookup because the by-name
	// uninstall route has to confirm the device exists before it queues anything.
	devices DeviceLookup
}

// DeviceLookup is the device surface this handler needs. devicemgmt.Repository
// satisfies it; so does a test double, which is why the handler depends on this
// and not on the concrete type.
type DeviceLookup interface {
	devicemgmt.SecretLookup
	GetByID(ctx context.Context, id string) (devicemgmt.Device, error)
}

func NewHandler(repo *Repository, hub HubDispatcher, auditLogger AuditLogger, storageDir string, authMW func(http.Handler) http.Handler, devices DeviceLookup) *Handler {
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
		r.With(rbac.RequireRole(rbac.RoleTechnician)).Put("/api/software/packages/{id}", h.updatePackage)
		r.With(rbac.RequireRole(rbac.RoleAdmin)).Delete("/api/software/packages/{id}", h.deletePackage)

		// Deployments
		r.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/software/deployments", h.listDeployments)
		r.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/software/deployments/{id}", h.getDeployment)
		r.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/software/deployments/{id}/tasks", h.listTasks)
		r.With(rbac.RequireRole(rbac.RoleTechnician)).Post("/api/software/deployments", h.createDeployment)

		// Remove one program named off a device's installed-software list.
		//
		// Not the catalog path, and deliberately so: that one demands a
		// package_id, and a program violating policy on an endpoint is by
		// definition one nobody deployed from the catalog.
		r.With(rbac.RequireRole(rbac.RoleTechnician)).Post("/api/devices/{id}/software/uninstall", h.uninstallDeviceSoftware)
		r.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/devices/{id}/commands/{cmdId}", h.getDeviceCommand)
	})

	// Agent Endpoints (Authenticated via device header or unauthenticated download token)
	r.Get("/api/agent/packages/{id}/download", h.downloadPackage)
	r.Post("/api/agent/tasks/{id}/progress", h.reportProgress)
}

// maxPackageBytes is the ceiling on an uploaded package.
//
// It is a var rather than a const only so the tests can exercise the refusal
// without writing half a gigabyte to a temp dir. Nothing at runtime changes it.
//
// ponytail: 500 MB is still generous for a package upload. Lower it if the
// fleet's real artifacts are smaller; nothing here depends on the value.
var maxPackageBytes int64 = 500 << 20

func (h *Handler) uploadPackage(w http.ResponseWriter, r *http.Request) {
	// The MaxBytesReader is the actual cap. ParseMultipartForm's argument is a
	// memory threshold, not a limit -- it is the size at which the parser spills
	// to disk, so passing 500 MB means "keep up to 500 MB in RAM", and a body of
	// any size is accepted, with the excess written to a temp file. The upload
	// route is authenticated, so this is a resource limit rather than a
	// pre-authentication one, but it is the same shape: the stated intent was a
	// limit and what was written was not one.
	r.Body = http.MaxBytesReader(w, r.Body, maxPackageBytes+(1<<20)) // +1 MB of form fields
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("package exceeds the %d MB limit", maxPackageBytes>>20))
			return
		}
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
	if msg, ok := checkExtensionMatches(pkgType, header.Filename); !ok {
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

type UpdatePackageRequest struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	InstallArgs   string `json:"install_args"`
	UninstallArgs string `json:"uninstall_args"`
}

func (h *Handler) updatePackage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req UpdatePackageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	pkg, err := h.repo.GetPackage(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "package not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = pkg.Name
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		version = pkg.Version
	}

	if pkg.PackageType == PkgTypeEXE && strings.TrimSpace(req.InstallArgs) == "" {
		writeErr(w, http.StatusBadRequest, "install_args is required for an .exe package")
		return
	}

	if err := h.repo.UpdatePackageArgs(r.Context(), id, name, version, req.InstallArgs, req.UninstallArgs); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	pkg.Name = name
	pkg.Version = version
	pkg.InstallArgs = req.InstallArgs
	pkg.UninstallArgs = req.UninstallArgs

	actorID := auth.UserIDFromContext(r.Context())
	if h.audit != nil {
		_ = h.audit.Log(r.Context(), "user", actorID, "software.update", id, map[string]string{
			"package_name": name,
		})
	}

	writeJSON(w, http.StatusOK, pkg)
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

	// An absent action means install. Anything else must be one of the two
	// verbs, so a typo does not silently become an install.
	if req.Action == "" {
		req.Action = ActionInstall
	}
	if req.Action != ActionInstall && req.Action != ActionUninstall {
		writeErr(w, http.StatusBadRequest, "action must be 'install' or 'uninstall'")
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

	// An agent that predates a command answers it with an error that lands in
	// agent_commands, not in deployment_tasks, so the task row never moves and the
	// orphan sweep does not reap it either -- it only reaps offline devices. The
	// task would sit in 'dispatched' forever on a perfectly healthy endpoint.
	// Refusing before any row exists is the only version of this that ends.
	command := "software.install"
	if req.Action == ActionUninstall {
		command = "software.uninstall"
	}
	missing, err := h.repo.DevicesWithoutCapability(r.Context(), deviceIDs, command)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to check endpoint capabilities: "+err.Error())
		return
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for _, d := range missing {
			label := d.OSName
			if label == "" {
				label = "unknown OS"
			}
			names = append(names, fmt.Sprintf("%s (%s)", d.ID, label))
		}
		writeErr(w, http.StatusBadRequest, fmt.Sprintf(
			"%d of %d target endpoint(s) run an agent that cannot handle %q: %s. "+
				"Update those agents first; the deployment was not created.",
			len(missing), len(deviceIDs), command, strings.Join(names, ", ")))
		return
	}

	depID := NewID()
	actorID := auth.UserIDFromContext(r.Context())
	now := time.Now().UTC()

	// An uninstall with no uninstall_args would be dispatched to run an
	// uninstaller with no switches, which is the exact thing this feature exists
	// to never do -- the same reason install_args is required for an .exe. It is
	// rejected here, at the boundary, rather than as a task failure the
	// operator has to discover after a fleet-wide rollout has already started.
	if req.Action == ActionUninstall && strings.TrimSpace(pkg.UninstallArgs) == "" {
		writeErr(w, http.StatusBadRequest,
			"this package has no uninstall_args: an uninstall would run the "+
				"uninstaller with no silent switches, which opens an interactive "+
				"window on the endpoint. Set uninstall_args on the package first "+
				"(for an MSI, /x with its ProductCode; for a native uninstaller, "+
				"its own silent switch such as /s)")
		return
	}

	dep := SoftwareDeployment{
		ID:         depID,
		PackageID:  req.PackageID,
		Action:     req.Action,
		Name:       req.Name,
		TargetType: req.TargetType,
		TargetID:   req.TargetID,
		CreatedBy:  actorID,
		Status:     "running",
		CreatedAt:  now,
	}
	if req.Action == ActionUninstall {
		dep.TaskArgs = pkg.UninstallArgs
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
			}
			if req.Action == ActionUninstall {
				// No download_url, no sha256, no file_name: the installer is
				// already on the endpoint and the agent re-derives its own
				// uninstall command from what it finds installed there. Sending
				// an installer path would be sending a file that should not exist.
				cmdPayload["package_type"] = pkg.PackageType
				cmdPayload["uninstall_args"] = pkg.UninstallArgs
			} else {
				cmdPayload["download_url"] = fmt.Sprintf("/api/agent/packages/%s/download", pkg.ID)
				cmdPayload["file_name"] = pkg.FileName
				cmdPayload["package_type"] = pkg.PackageType
				cmdPayload["sha256"] = pkg.SHA256
				cmdPayload["install_args"] = pkg.InstallArgs
			}
			env := transport.Envelope{
				Type:    transport.TypeCommand,
				ID:      NewID(),
				Command: command,
				Payload: cmdPayload,
			}
			envBytes, _ := json.Marshal(env)

			if h.hub.SendTo(task.DeviceID, envBytes) {
				// task.DeviceID is the owner of this row, not the operator's
				// device: the dispatch was aimed at it, so it is the only
				// identity allowed to mark it dispatched.
				_ = h.repo.UpdateTaskProgress(r.Context(), task.DeviceID, TaskProgressReport{
					TaskID: task.ID,
					Status: TaskStatusDispatched,
				})
				dispatchedCount++
			}
		}
	}

	if h.audit != nil {
		_ = h.audit.Log(r.Context(), "user", actorID, "software."+req.Action, dep.ID, map[string]string{
			"deployment_name": dep.Name, "package_id": pkg.ID, "targets_total": strconv.Itoa(len(tasks)), "dispatched_now": strconv.Itoa(dispatchedCount),
		})
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"deployment":      dep,
		"tasks_total":     len(tasks),
		"dispatched_live": dispatchedCount,
	})
}

// uninstallDeviceSoftware requests removal of one program named off a device's
// installed-software list.
//
// The body carries no uninstall arguments, and that absence is the design rather
// than a gap. There is no universal silent switch for a Windows uninstaller --
// NSIS wants /S, Inno Setup wants /VERYSILENT, WinRAR wants /s -- so an operator
// asked for switches would be guessing, and a wrong guess is what opens a window
// on the endpoint. The agent resolves the switches from the endpoint's own
// registry and refuses when it cannot verify them, so the operator supplies a
// name and nothing else.
//
// What this response does NOT say is that anything was removed. It returns 201
// when the command was handed to the agent, which is a different moment from the
// uninstall finishing. The real outcome -- including a refusal, which is the
// common case for a program with no recorded quiet command -- arrives later in
// agent_commands.result. So the console must present this as "sent".
func (h *Handler) uninstallDeviceSoftware(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")

	var req struct {
		SoftwareName string `json:"software_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.SoftwareName)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "software_name is required")
		return
	}

	if _, err := h.devices.GetByID(r.Context(), deviceID); err != nil {
		if errors.Is(err, devicemgmt.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "device not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// An agent that predates this command would drop it silently, and its row
	// would sit in 'sent' forever on a healthy endpoint: the reply lands in
	// agent_commands, not in deployment_tasks, so the orphan sweep -- which only
	// reaps offline devices -- never touches it. Refusing here is the only
	// version of that story that ends.
	const command = "software.uninstall.by_name"
	missing, err := h.repo.DevicesWithoutCapability(r.Context(), []string{deviceID}, command)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to check endpoint capabilities: "+err.Error())
		return
	}
	if len(missing) > 0 {
		label := missing[0].OSName
		if label == "" {
			label = "unknown OS"
		}
		writeErr(w, http.StatusBadRequest, fmt.Sprintf(
			"endpoint %s (%s) runs an agent that cannot handle %q. Update that agent first; "+
				"nothing was uninstalled.", deviceID, label, command))
		return
	}

	// Offline is a refusal, not a queue. The operator is standing on the device's
	// page looking at a program that is still there; answering 202 "queued" for a
	// command no agent will ever read teaches them that pressing the button works.
	if !h.hub.Online(deviceID) {
		writeErr(w, http.StatusConflict,
			"endpoint is offline: nothing was uninstalled. Try again once it reconnects.")
		return
	}

	cmdID := NewID()
	payload := fmt.Sprintf(`{"software_name":%q}`, name)
	if err := h.repo.CreateAgentCommand(r.Context(), cmdID, deviceID, command, payload); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	envBytes, err := json.Marshal(transport.Envelope{
		Type:    transport.TypeCommand,
		ID:      cmdID,
		Command: command,
		Payload: map[string]string{"software_name": name},
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !h.hub.SendTo(deviceID, envBytes) {
		// The row exists and says 'sent' while nothing reached the agent. Left
		// that way it is honest -- it never claims a removal -- so it is not
		// deleted here. The operator is told the truth about this attempt.
		writeErr(w, http.StatusConflict,
			"endpoint connection is busy; the uninstall was not sent. Try again.")
		return
	}

	if h.audit != nil {
		_ = h.audit.Log(r.Context(), "user", auth.UserIDFromContext(r.Context()),
			"software.uninstall.requested", deviceID,
			map[string]string{"software_name": name, "command_id": cmdID})
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"status":        "sent",
		"command_id":    cmdID,
		"software_name": name,
	})
}

func (h *Handler) getDeviceCommand(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	cmdID := chi.URLParam(r, "cmdId")
	cmd, err := h.repo.GetAgentCommand(r.Context(), cmdID, deviceID)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "command not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cmd)
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
	// The authenticated device is the only device whose tasks this may write.
	// The task id comes from the URL, so without folding the identity into the
	// update itself, any enrolled agent could report progress for any other.
	deviceID, ok := devicemgmt.AuthenticateAgent(w, r, h.devices)
	if !ok {
		return
	}
	taskID := chi.URLParam(r, "id")
	var rep TaskProgressReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	rep.TaskID = taskID

	if err := h.repo.UpdateTaskProgress(r.Context(), deviceID, rep); err != nil {
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

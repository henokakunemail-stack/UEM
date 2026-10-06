package patchmgmt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

type AuditLogger interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

type DeviceValidator interface {
	FindBySecretHash(ctx context.Context, secretHash string) (devicemgmt.Device, error)
	GetByID(ctx context.Context, id string) (devicemgmt.Device, error)
	UpdateLastPatchScan(ctx context.Context, id string, at time.Time) error
}

type Handler struct {
	repo           *Repository
	hub            *transport.Hub
	audit          AuditLogger
	jwtSvc         *auth.JWTService
	authMiddleware func(http.Handler) http.Handler
	devices        DeviceValidator
}

func NewHandler(
	repo *Repository,
	hub *transport.Hub,
	audit AuditLogger,
	jwtSvc *auth.JWTService,
	authMiddleware func(http.Handler) http.Handler,
	devices DeviceValidator,
) *Handler {
	return &Handler{
		repo:           repo,
		hub:            hub,
		audit:          audit,
		jwtSvc:         jwtSvc,
		authMiddleware: authMiddleware,
		devices:        devices,
	}
}

func (h *Handler) Register(r chi.Router) {
	// Summary & Fleet Patches (Viewer+)
	r.With(h.authMiddleware).Get("/api/patches/summary", h.getFleetSummary)
	r.With(h.authMiddleware).Get("/api/patches", h.listFleetPatches)

	// Device Patches
	r.With(h.authMiddleware).Get("/api/devices/{id}/patches", h.listDevicePatches)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).
		Post("/api/devices/{id}/patches/scan", h.triggerScan)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).
		Post("/api/devices/{id}/patches/install", h.installPatches)

	// Patch Jobs
	r.With(h.authMiddleware).Get("/api/devices/{id}/patches/jobs", h.listInstallJobs)
	r.With(h.authMiddleware).Get("/api/devices/{id}/patches/jobs/{jobId}", h.getInstallJob)

	// Agent Endpoints
	r.Post("/api/agent/patches/scan-report", h.reportScanResults)
	r.Post("/api/agent/patches/install-result", h.reportInstallResult)
}

func (h *Handler) getFleetSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := h.repo.GetFleetSummary(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) listFleetPatches(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	patches, err := h.repo.ListFleetPatches(r.Context(), 100, state)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if patches == nil {
		patches = []DevicePatch{}
	}
	writeJSON(w, http.StatusOK, patches)
}

func (h *Handler) listDevicePatches(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	state := r.URL.Query().Get("state")
	patches, err := h.repo.ListDevicePatches(r.Context(), deviceID, state)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if patches == nil {
		patches = []DevicePatch{}
	}
	writeJSON(w, http.StatusOK, patches)
}

func (h *Handler) triggerScan(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if _, err := h.devices.GetByID(r.Context(), deviceID); err != nil {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}

	actorID := auth.UserIDFromContext(r.Context())

	if h.hub == nil || !h.hub.Online(deviceID) {
		writeErr(w, http.StatusConflict, "device is currently offline")
		return
	}

	scanEnv := transport.Envelope{
		Type:    transport.TypeCommand,
		ID:      NewID(),
		Command: "patch.scan",
		Payload: map[string]any{},
	}
	b, err := json.Marshal(scanEnv)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to marshal scan envelope")
		return
	}

	if !h.hub.SendTo(deviceID, b) {
		writeErr(w, http.StatusConflict, "failed to send scan command to agent")
		return
	}

	_ = h.audit.Log(r.Context(), "user", actorID, "patch.scan", deviceID, map[string]string{
		"action": "triggered_scan",
	})

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "dispatched",
		"message": "patch scan dispatched to agent",
	})
}

func (h *Handler) installPatches(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if _, err := h.devices.GetByID(r.Context(), deviceID); err != nil {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}

	actorID := auth.UserIDFromContext(r.Context())

	var req InstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json payload")
		return
	}

	if len(req.PatchIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "patch_ids cannot be empty")
		return
	}

	if req.RebootPolicy == "" {
		req.RebootPolicy = RebootPolicyNoReboot
	}

	if h.hub == nil || !h.hub.Online(deviceID) {
		writeErr(w, http.StatusConflict, "device is currently offline")
		return
	}

	jobID := NewID()
	patchIDsJSON, _ := json.Marshal(req.PatchIDs)

	job := &PatchInstallJob{
		ID:           jobID,
		DeviceID:     deviceID,
		OperatorID:   actorID,
		PatchIDs:     string(patchIDsJSON),
		Status:       JobStatusDispatched,
		RebootPolicy: req.RebootPolicy,
		StartedAt:    time.Now().UTC(),
	}

	if err := h.repo.CreateInstallJob(r.Context(), job); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Dispatch to agent via WebSocket
	cmdPayload := map[string]any{
		"job_id":        jobID,
		"patch_ids":     req.PatchIDs,
		"reboot_policy": req.RebootPolicy,
	}
	installEnv := transport.Envelope{
		Type:    transport.TypeCommand,
		ID:      jobID,
		Command: "patch.install",
		Payload: cmdPayload,
	}
	b, _ := json.Marshal(installEnv)
	if !h.hub.SendTo(deviceID, b) {
		writeErr(w, http.StatusConflict, "failed to dispatch install command to agent")
		return
	}

	_ = h.audit.Log(r.Context(), "user", actorID, "patch.install", deviceID, map[string]string{
		"job_id":        jobID,
		"patch_count":   fmt.Sprintf("%d", len(req.PatchIDs)),
		"reboot_policy": req.RebootPolicy,
	})

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "dispatched",
		"job_id": jobID,
		"job":    job,
	})
}

func (h *Handler) listInstallJobs(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	jobs, err := h.repo.ListInstallJobs(r.Context(), deviceID, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if jobs == nil {
		jobs = []PatchInstallJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (h *Handler) getInstallJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobId")
	job, err := h.repo.GetInstallJob(r.Context(), jobID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "job not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) reportScanResults(w http.ResponseWriter, r *http.Request) {
	deviceID := r.Header.Get("X-Device-Id")
	secret := r.Header.Get("X-Device-Secret")

	if deviceID == "" || secret == "" {
		writeErr(w, http.StatusUnauthorized, "missing device credentials")
		return
	}

	dev, err := h.devices.FindBySecretHash(r.Context(), devicemgmt.HashToken(secret))
	if err != nil || dev.ID != deviceID {
		writeErr(w, http.StatusUnauthorized, "invalid device credentials")
		return
	}

	var rep PatchScanReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json payload")
		return
	}

	if err := h.repo.UpsertPatches(r.Context(), deviceID, rep.Patches); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Stamp only after the rows landed. This is what lets the console tell an
	// unpatched device from one nobody has scanned: both have zero pending rows,
	// and without a timestamp the panel says the same thing for both.
	if err := h.devices.UpdateLastPatchScan(r.Context(), deviceID, time.Now()); err != nil {
		// The scan itself is stored and the report is a success. Losing the
		// timestamp costs the console its "last scanned" line and nothing else,
		// so this must not fail the agent's report.
		log.Warn().Err(err).Str("device", deviceID).Msg("could not stamp last patch scan")
	}

	_ = h.audit.Log(r.Context(), "agent", deviceID, "patch.reported", deviceID, map[string]string{
		"patch_count": fmt.Sprintf("%d", len(rep.Patches)),
	})

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "recorded",
		"message": fmt.Sprintf("%d patches recorded", len(rep.Patches)),
	})
}

func (h *Handler) reportInstallResult(w http.ResponseWriter, r *http.Request) {
	deviceID := r.Header.Get("X-Device-Id")
	secret := r.Header.Get("X-Device-Secret")

	if deviceID == "" || secret == "" {
		writeErr(w, http.StatusUnauthorized, "missing device credentials")
		return
	}

	dev, err := h.devices.FindBySecretHash(r.Context(), devicemgmt.HashToken(secret))
	if err != nil || dev.ID != deviceID {
		writeErr(w, http.StatusUnauthorized, "invalid device credentials")
		return
	}

	var rep PatchInstallReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json payload")
		return
	}

	if rep.JobID == "" {
		writeErr(w, http.StatusBadRequest, "missing job_id")
		return
	}

	if err := h.repo.UpdateInstallJobResult(r.Context(), deviceID, rep); err != nil {
		// 404, not 500: the job is either gone or belongs to another device, and
		// the two are deliberately indistinguishable so this endpoint cannot be
		// used to enumerate which install jobs exist. A 500 here would also have
		// told the agent its report was a server fault, which invites a retry
		// loop against a refusal that will never succeed.
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "install job not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.audit.Log(r.Context(), "agent", deviceID, "patch.result", rep.JobID, map[string]string{
		"status": rep.Status,
	})

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "recorded",
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

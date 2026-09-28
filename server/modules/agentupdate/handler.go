package agentupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

type Auditor interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

type Handler struct {
	repo       *Repository
	hub        *transport.Hub
	deviceRepo *devicemgmt.Repository
	auditor    Auditor
	storageDir string
	authMw     func(http.Handler) http.Handler
}

func NewHandler(
	repo *Repository,
	hub *transport.Hub,
	deviceRepo *devicemgmt.Repository,
	auditor Auditor,
	storageDir string,
	authMw func(http.Handler) http.Handler,
) *Handler {
	_ = os.MkdirAll(storageDir, 0755)
	return &Handler{
		repo:       repo,
		hub:        hub,
		deviceRepo: deviceRepo,
		auditor:    auditor,
		storageDir: storageDir,
		authMw:     authMw,
	}
}

func (h *Handler) Register(r chi.Router) {
	// Agent endpoints (secret/token verified or device authenticated)
	r.Post("/api/agent/devices/{id}/update/report", h.handleAgentReport)
	r.Get("/api/agent/releases/{id}/download", h.handleAgentDownload)

	// Operator Console API
	r.Group(func(cr chi.Router) {
		cr.Use(h.authMw)

		// Releases
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/releases", h.handleListReleases)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/releases/{id}", h.handleGetRelease)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/releases/{id}/download", h.handleDownloadRelease)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/agent-updates/releases", h.handleUploadRelease)

		// Campaigns
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/campaigns", h.handleListCampaigns)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/agent-updates/campaigns/{id}", h.handleGetCampaign)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/agent-updates/campaigns", h.handleCreateCampaign)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/agent-updates/campaigns/{id}/start", h.handleStartCampaign)

		// Device Update Controls
		cr.With(rbac.RequireRole(rbac.RoleTechnician)).Post("/api/devices/{id}/update/dispatch", h.handleDispatchDeviceUpdate)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/devices/{id}/update/status", h.handleGetDeviceUpdateStatus)
	})
}

func (h *Handler) handleUploadRelease(w http.ResponseWriter, r *http.Request) {
	err := r.ParseMultipartForm(100 << 20) // 100 MB max
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to parse multipart form"})
		return
	}

	version := r.FormValue("version")
	osName := r.FormValue("os_name")
	arch := r.FormValue("arch")
	changelog := r.FormValue("changelog")

	if version == "" || osName == "" || arch == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version, os_name, and arch are required"})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file field is required"})
		return
	}
	defer file.Close()

	relID := newID()
	destPath := filepath.Join(h.storageDir, fmt.Sprintf("%s_%s_%s_%s", version, osName, arch, filepath.Base(header.Filename)))

	destFile, err := os.Create(destPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create destination file"})
		return
	}
	defer destFile.Close()

	hasher := sha256.New()
	writer := io.MultiWriter(destFile, hasher)

	written, err := io.Copy(writer, file)
	if err != nil {
		_ = os.Remove(destPath)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to write release file"})
		return
	}

	checksum := hex.EncodeToString(hasher.Sum(nil))
	actorID := auth.UserIDFromContext(r.Context())

	release := &AgentRelease{
		ID:             relID,
		Version:        version,
		OSName:         osName,
		Arch:           arch,
		FilePath:       destPath,
		FileSize:       written,
		SHA256Checksum: checksum,
		Changelog:      changelog,
		IsActive:       true,
		UploadedBy:     actorID,
	}

	if err := h.repo.CreateRelease(r.Context(), release); err != nil {
		_ = os.Remove(destPath)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	_ = h.auditor.Log(r.Context(), "user", actorID, "agent_update.release_upload", relID, map[string]string{
		"version":  version,
		"os_name":  osName,
		"arch":     arch,
		"checksum": checksum,
	})

	writeJSON(w, http.StatusCreated, release.toDTO())
}

func (h *Handler) handleListReleases(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListReleases(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Never hand the browser the server-side absolute path.
	out := make([]AgentReleaseDTO, 0, len(list))
	for _, rel := range list {
		out = append(out, rel.toDTO())
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) handleGetRelease(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rel, err := h.repo.GetRelease(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "release not found"})
		return
	}
	writeJSON(w, http.StatusOK, rel.toDTO())
}

func (h *Handler) handleDownloadRelease(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rel, err := h.repo.GetRelease(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "release not found"})
		return
	}
	http.ServeFile(w, r, rel.FilePath)
}

func (h *Handler) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	if _, ok := devicemgmt.AuthenticateAgent(w, r, h.deviceRepo); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	rel, err := h.repo.GetRelease(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "release not found"})
		return
	}
	http.ServeFile(w, r, rel.FilePath)
}

func (h *Handler) handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name               string `json:"name"`
		Description        string `json:"description"`
		TargetVersion      string `json:"target_version"`
		TargetType         string `json:"target_type"`
		TargetID           string `json:"target_id"`
		BatchSize          int    `json:"batch_size"`
		StaggerIntervalSec int    `json:"stagger_interval_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	if req.Name == "" || req.TargetVersion == "" || req.TargetType == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name, target_version, and target_type are required"})
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	campaign := &UpdateCampaign{
		Name:               req.Name,
		Description:        req.Description,
		TargetVersion:      req.TargetVersion,
		TargetType:         req.TargetType,
		TargetID:           req.TargetID,
		BatchSize:          req.BatchSize,
		StaggerIntervalSec: req.StaggerIntervalSec,
		CreatedBy:          actorID,
	}

	if err := h.repo.CreateCampaign(r.Context(), campaign); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	_ = h.auditor.Log(r.Context(), "user", actorID, "agent_update.campaign_create", campaign.ID, map[string]string{
		"name":           campaign.Name,
		"target_version": campaign.TargetVersion,
	})

	writeJSON(w, http.StatusCreated, campaign)
}

func (h *Handler) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListCampaigns(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) handleGetCampaign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := h.repo.GetCampaign(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "campaign not found"})
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) handleStartCampaign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	campaign, err := h.repo.GetCampaign(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "campaign not found"})
		return
	}

	deviceIDs, err := h.repo.ResolveTargetDevices(r.Context(), campaign.TargetType, campaign.TargetID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("resolve devices: %v", err)})
		return
	}

	_ = h.repo.UpdateCampaignStatus(r.Context(), id, "in_progress")
	actorID := auth.UserIDFromContext(r.Context())

	dispatchedCount := 0
	for _, devID := range deviceIDs {
		dev, err := h.deviceRepo.GetByID(r.Context(), devID)
		if err != nil {
			continue
		}
		if dev.AgentVersionString() == campaign.TargetVersion {
			continue // already at target version
		}

		// Lookup release for this device OS and arch
		arch := "amd64" // default
		rel, err := h.repo.GetActiveReleaseForDevice(r.Context(), campaign.TargetVersion, dev.OSName, arch)
		if err != nil {
			log.Warn().Err(err).Str("device", devID).Msg("no active release for device")
			continue
		}

		now := time.Now().UTC()
		fromVersion := dev.AgentVersionString()
		task := &DeviceUpdateTask{
			CampaignID:    &campaign.ID,
			DeviceID:      devID,
			FromVersion:   fromVersion,
			TargetVersion: campaign.TargetVersion,
			Status:        "dispatched",
			DispatchedAt:  &now,
		}
		if err := h.repo.CreateUpdateTask(r.Context(), task); err != nil {
			continue
		}

		if h.hub.Online(devID) {
			payload := map[string]any{
				"task_id":          task.ID,
				"target_version":   campaign.TargetVersion,
				"download_url":     fmt.Sprintf("/api/agent/releases/%s/download", rel.ID),
				"sha256_checksum":  rel.SHA256Checksum,
				"file_size":        rel.FileSize,
			}
			msg, _ := json.Marshal(map[string]any{
				"type":    "command",
				"id":      task.ID,
				"command": "update.apply",
				"payload": payload,
			})
			if h.hub.SendTo(devID, msg) {
				dispatchedCount++
			}
		}
	}

	_ = h.auditor.Log(r.Context(), "user", actorID, "agent_update.campaign_start", id, map[string]string{
		"dispatched": fmt.Sprintf("%d", dispatchedCount),
		"target":     campaign.TargetType,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "started",
		"total_targets":    len(deviceIDs),
		"dispatched_live":  dispatchedCount,
	})
}

func (h *Handler) handleDispatchDeviceUpdate(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	dev, err := h.deviceRepo.GetByID(r.Context(), deviceID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}

	var req struct {
		TargetVersion string `json:"target_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.TargetVersion == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "target_version is required"})
		return
	}

	arch := "amd64"
	rel, err := h.repo.GetActiveReleaseForDevice(r.Context(), req.TargetVersion, dev.OSName, arch)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("no active release found for %s/%s/%s", req.TargetVersion, dev.OSName, arch),
		})
		return
	}

	now := time.Now().UTC()
	fromVersion := dev.AgentVersionString()
	task := &DeviceUpdateTask{
		DeviceID:      deviceID,
		FromVersion:   fromVersion,
		TargetVersion: req.TargetVersion,
		Status:        "dispatched",
		DispatchedAt:  &now,
	}
	if err := h.repo.CreateUpdateTask(r.Context(), task); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if !h.hub.Online(deviceID) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "queued",
			"task_id": task.ID,
			"message": "device is currently offline; update queued",
		})
		return
	}

	payload := map[string]any{
		"task_id":         task.ID,
		"target_version":  req.TargetVersion,
		"download_url":    fmt.Sprintf("/api/agent/releases/%s/download", rel.ID),
		"sha256_checksum": rel.SHA256Checksum,
		"file_size":       rel.FileSize,
	}
	msg, _ := json.Marshal(map[string]any{
		"type":    "command",
		"id":      task.ID,
		"command": "update.apply",
		"payload": payload,
	})
	if !h.hub.SendTo(deviceID, msg) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "device connection busy"})
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "agent_update.device_dispatched", deviceID, map[string]string{
		"task_id":        task.ID,
		"target_version": req.TargetVersion,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "dispatched",
		"task_id":        task.ID,
		"target_version": req.TargetVersion,
	})
}

func (h *Handler) handleGetDeviceUpdateStatus(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	task, err := h.repo.GetLatestTaskForDevice(r.Context(), deviceID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no update task found for device"})
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (h *Handler) handleAgentReport(w http.ResponseWriter, r *http.Request) {
	// Without this, any client that can reach the port could mark a rollout
	// successful and overwrite devices.agent_version for any device ID.
	if _, ok := devicemgmt.AuthenticateAgent(w, r, h.deviceRepo); !ok {
		return
	}
	deviceID := chi.URLParam(r, "id")
	var req struct {
		TaskID        string `json:"task_id"`
		Status        string `json:"status"` // 'downloading', 'verifying', 'swapping', 'success', 'rollback', 'failed'
		TargetVersion string `json:"target_version"`
		ErrorMessage  string `json:"error_message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	if err := h.repo.RecordTaskProgress(r.Context(), req.TaskID, req.Status, req.ErrorMessage); err != nil {
		log.Warn().Err(err).Str("task_id", req.TaskID).Msg("failed to update task progress")
	}

	if req.Status == "success" && req.TargetVersion != "" {
		_ = h.repo.UpdateDeviceAgentVersion(r.Context(), deviceID, req.TargetVersion)
		_ = h.auditor.Log(r.Context(), "device", deviceID, "agent_update.completed", deviceID, map[string]string{
			"version": req.TargetVersion,
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

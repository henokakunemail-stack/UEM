package systempolicy

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// Compile-time proof Handler satisfies SystemPolicySyncer.
var _ transport.SystemPolicySyncer = (*Handler)(nil)

type Hub interface {
	Online(deviceID string) bool
	SendTo(deviceID string, msg []byte) bool
}

type AuditLogger interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

type Handler struct {
	repo           *Repository
	hub            Hub
	devices        *devicemgmt.Repository
	audit          AuditLogger
	authMiddleware func(http.Handler) http.Handler
	// wallpaperDir is where uploaded wallpapers are stored. See WithDataDir.
	wallpaperDir string
}

// WithDataDir stores wallpapers under dataDir. The default is the working
// directory's data folder, which is what it has always been.
func (h *Handler) WithDataDir(dataDir string) *Handler {
	h.wallpaperDir = filepath.Join(dataDir, "wallpapers")
	return h
}

func NewHandler(
	repo *Repository,
	hub Hub,
	devices *devicemgmt.Repository,
	audit AuditLogger,
	authMiddleware func(http.Handler) http.Handler,
) *Handler {
	return &Handler{
		repo:           repo,
		hub:            hub,
		devices:        devices,
		audit:          audit,
		authMiddleware: authMiddleware,
		wallpaperDir:   filepath.Join("data", "wallpapers"),
	}
}

func (h *Handler) Register(r chi.Router) {
	// Policy CRUD & Management
	r.With(h.authMiddleware).Get("/api/system-policies", h.listPolicies)
	r.With(h.authMiddleware).Get("/api/system-policies/{id}", h.getPolicy)

	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).
		Post("/api/system-policies", h.createPolicy)

	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).
		Put("/api/system-policies/{id}", h.updatePolicy)

	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).
		Delete("/api/system-policies/{id}", h.deletePolicy)

	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).
		Post("/api/system-policies/{id}/apply", h.applyPolicyNow)

	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).
		Post("/api/system-policies/wallpaper-upload", h.wallpaperUpload)

	r.Get("/api/system-policies/wallpapers/{filename}", h.serveWallpaper)

	// Device-level State & Sync
	r.With(h.authMiddleware).
		Get("/api/devices/{id}/system-policy", h.getDeviceSystemPolicy)

	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).
		Post("/api/devices/{id}/system-policy/sync", h.syncDevicePolicy)

	r.With(h.authMiddleware).
		Get("/api/system-policies/states", h.listDeviceStates)

	// Agent reporting endpoint (Device Auth)
	r.Post("/api/agent/devices/{id}/system-policy/report", h.agentReportPolicyState)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeErrCode adds a machine-readable code the console translates.
func writeErrCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func (h *Handler) listPolicies(w http.ResponseWriter, r *http.Request) {
	policies, err := h.repo.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": redactPolicies(policies), "count": len(policies)})
}

func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	policy, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if policy == nil {
		writeErr(w, http.StatusNotFound, "system policy not found")
		return
	}
	writeJSON(w, http.StatusOK, redactPolicy(policy))
}

type createPolicyReq struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	TargetType  string          `json:"target_type"`
	TargetID    string          `json:"target_id"`
	IsEnabled   *bool           `json:"is_enabled"`
	Priority    int             `json:"priority"`
	Settings    json.RawMessage `json:"settings"`
}

// validateConfig checks the values a policy decides. Only what is set is
// checked: "not set" is always valid.
func validateConfig(c PolicyConfig) (code, msg string) {
	if c.USBStorageMode != nil {
		switch *c.USBStorageMode {
		case USBStorageAllow, USBStorageBlock, USBStorageReadOnly:
		default:
			return "invalid_usb_mode", "usb_storage_mode must be allow, block or read_only"
		}
	}
	if c.WindowsUpdateMode != nil {
		switch *c.WindowsUpdateMode {
		case WindowsUpdateDefault, WindowsUpdateAutoInstall, WindowsUpdateNotifyOnly:
		default:
			return "invalid_update_mode", "windows_update_mode must be default, auto_install or notify_only"
		}
	}
	if c.WallpaperEnabled != nil && *c.WallpaperEnabled {
		if strings.TrimSpace(c.WallpaperURL) == "" {
			return "wallpaper_url_required", "wallpaper_url is required when the wallpaper is enforced"
		}
		switch c.WallpaperStyle {
		case "", WallpaperFill, WallpaperFit, WallpaperStretch, WallpaperCenter, WallpaperTile:
		default:
			return "invalid_wallpaper_style", "wallpaper_style must be fill, fit, stretch, center or tile"
		}
	}
	for _, m := range []*int{c.InactivityTimeoutMinutes, c.ScreenSaverTimeoutMinutes} {
		if m != nil && (*m < 0 || *m > 24*60) {
			return "invalid_timeout", "timeouts are minutes between 0 (off) and 1440"
		}
	}
	if c.AdminAccountEnabled != nil && *c.AdminAccountEnabled {
		if strings.TrimSpace(c.AdminAccountUsername) == "" {
			return "admin_username_required", "admin_account_username is required when the managed admin account is enabled"
		}
		if c.AdminAccountPassword == "" {
			return "admin_password_required", "admin_account_password is required when the managed admin account is enabled"
		}
	}
	return "", ""
}

func (h *Handler) createPolicy(w http.ResponseWriter, r *http.Request) {
	var req createPolicyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	targetType := strings.ToLower(strings.TrimSpace(req.TargetType))
	if targetType != "all" && targetType != "group" && targetType != "device" {
		writeErr(w, http.StatusBadRequest, "target_type must be 'all', 'group', or 'device'")
		return
	}
	if (targetType == "group" || targetType == "device") && strings.TrimSpace(req.TargetID) == "" {
		writeErr(w, http.StatusBadRequest, "target_id is required for group or device target")
		return
	}

	isEnabled := true
	if req.IsEnabled != nil {
		isEnabled = *req.IsEnabled
	}
	priority := req.Priority
	if priority <= 0 {
		priority = 100
	}

	// No defaults are filled in: a setting the request leaves out is "not set".
	// Filling usb_storage_mode with "allow" here is what let any policy lift a
	// USB block it was never written to touch.
	settings, err := decodePolicyConfig(req.Settings)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid settings")
		return
	}
	keepStoredPassword(&settings, PolicyConfig{})
	if code, msg := validateConfig(settings); code != "" {
		writeErrCode(w, http.StatusBadRequest, code, msg)
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	p := &SystemPolicy{
		Name:        strings.TrimSpace(req.Name),
		Description: strings.TrimSpace(req.Description),
		TargetType:  targetType,
		TargetID:    strings.TrimSpace(req.TargetID),
		IsEnabled:   isEnabled,
		Priority:    priority,
		Settings:    settings,
		CreatedBy:   actorID,
	}

	if err := h.repo.Create(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if h.audit != nil {
		_ = h.audit.Log(r.Context(), "user", actorID, "system_policy.create", p.ID, map[string]string{
			"name": p.Name, "target_type": p.TargetType, "target_id": p.TargetID,
		})
	}

	// If enabled, automatically dispatch to affected targets
	if p.IsEnabled {
		go h.dispatchAffected(context.Background(), p.TargetType, p.TargetID)
	}

	writeJSON(w, http.StatusCreated, redactPolicy(p))
}

type updatePolicyReq struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	TargetType  *string         `json:"target_type"`
	TargetID    *string         `json:"target_id"`
	IsEnabled   *bool           `json:"is_enabled"`
	Priority    *int            `json:"priority"`
	Settings    json.RawMessage `json:"settings"`
}

func (h *Handler) updatePolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		writeErr(w, http.StatusNotFound, "system policy not found")
		return
	}

	var req updatePolicyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	oldTargetType, oldTargetID := p.TargetType, p.TargetID

	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		p.Name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		p.Description = strings.TrimSpace(*req.Description)
	}
	if req.TargetType != nil {
		tt := strings.ToLower(strings.TrimSpace(*req.TargetType))
		if tt != "all" && tt != "group" && tt != "device" {
			writeErr(w, http.StatusBadRequest, "target_type must be 'all', 'group', or 'device'")
			return
		}
		p.TargetType = tt
	}
	if req.TargetID != nil {
		p.TargetID = strings.TrimSpace(*req.TargetID)
	}
	if (p.TargetType == "group" || p.TargetType == "device") && p.TargetID == "" {
		writeErr(w, http.StatusBadRequest, "target_id is required for group or device target")
		return
	}
	if req.IsEnabled != nil {
		p.IsEnabled = *req.IsEnabled
	}
	if req.Priority != nil && *req.Priority > 0 {
		p.Priority = *req.Priority
	}
	if len(req.Settings) > 0 {
		settings, err := decodePolicyConfig(req.Settings)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid settings")
			return
		}
		keepStoredPassword(&settings, p.Settings)
		if code, msg := validateConfig(settings); code != "" {
			writeErrCode(w, http.StatusBadRequest, code, msg)
			return
		}
		p.Settings = settings
	}

	if err := h.repo.Update(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	if h.audit != nil {
		_ = h.audit.Log(r.Context(), "user", actorID, "system_policy.update", p.ID, map[string]string{
			"name": p.Name, "target_type": p.TargetType, "target_id": p.TargetID,
		})
	}

	// Dispatch to both old and new targets if scope changed or policy is active
	go func() {
		h.dispatchAffected(context.Background(), oldTargetType, oldTargetID)
		if oldTargetType != p.TargetType || oldTargetID != p.TargetID {
			h.dispatchAffected(context.Background(), p.TargetType, p.TargetID)
		}
	}()

	writeJSON(w, http.StatusOK, redactPolicy(p))
}

func (h *Handler) deletePolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		writeErr(w, http.StatusNotFound, "system policy not found")
		return
	}

	if err := h.repo.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	if h.audit != nil {
		_ = h.audit.Log(r.Context(), "user", actorID, "system_policy.delete", id, map[string]string{
			"name": p.Name, "target_type": p.TargetType, "target_id": p.TargetID,
		})
	}

	// Re-evaluate affected devices after policy removal
	go h.dispatchAffected(context.Background(), p.TargetType, p.TargetID)

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) applyPolicyNow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		writeErr(w, http.StatusNotFound, "system policy not found")
		return
	}

	go h.dispatchAffected(context.Background(), p.TargetType, p.TargetID)

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "applying",
		"message": fmt.Sprintf("Applying policy %s to target %s", p.Name, p.TargetType),
	})
}

func (h *Handler) getDeviceSystemPolicy(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	compiled, sources, matched, err := h.repo.CompileEffectivePolicyWithSources(r.Context(), deviceID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	state, err := h.repo.GetDeviceState(r.Context(), deviceID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// sources says which policy decided each setting; a setting missing from
	// it is decided by nobody and stays at the OS default.
	writeJSON(w, http.StatusOK, map[string]any{
		"compiled":         redactCompiled(compiled),
		"state":            redactState(state),
		"sources":          sources,
		"matched_policies": matched,
	})
}

func (h *Handler) syncDevicePolicy(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if h.hub == nil || !h.hub.Online(deviceID) {
		writeErr(w, http.StatusConflict, "device is offline")
		return
	}

	if err := h.dispatchDevice(r.Context(), deviceID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	if h.audit != nil {
		_ = h.audit.Log(r.Context(), "user", actorID, "system_policy.sync", deviceID, nil)
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "dispatched",
		"message": "Policy dispatched to device",
	})
}

func (h *Handler) listDeviceStates(w http.ResponseWriter, r *http.Request) {
	states, err := h.repo.ListDeviceStates(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"states": redactStates(states), "count": len(states)})
}

func (h *Handler) agentReportPolicyState(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")

	// Validate agent authorization headers
	if h.devices != nil {
		authedID, ok := devicemgmt.AuthenticateAgent(w, r, h.devices)
		if !ok {
			return
		}
		if authedID != deviceID {
			writeErr(w, http.StatusForbidden, "device ID mismatch")
			return
		}
	}

	var req PolicyReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	status := req.Status
	if status != "synced" && status != "failed" {
		status = "synced"
	}

	state := &DeviceSystemPolicyState{
		DeviceID:        deviceID,
		PolicyVersion:   req.PolicyVersion,
		Status:          status,
		AppliedSettings: req.Settings,
		ErrorMessage:    req.ErrorMessage,
	}

	if err := h.repo.UpdateDeviceState(r.Context(), state); err != nil {
		log.Error().Err(err).Str("device", deviceID).Msg("update device policy state failed")
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	log.Info().Str("device", deviceID).Str("version", req.PolicyVersion).Str("status", status).Msg("policy report recorded")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) dispatchDevice(ctx context.Context, deviceID string) error {
	if h.hub == nil || !h.hub.Online(deviceID) {
		return nil
	}
	compiled, err := h.repo.CompileEffectivePolicy(ctx, deviceID)
	if err != nil {
		return fmt.Errorf("compile policy for %s: %w", deviceID, err)
	}

	envBytes, err := systemPolicyApplyEnvelope(compiled)
	if err != nil {
		return fmt.Errorf("encode system policy envelope: %w", err)
	}

	if !h.hub.SendTo(deviceID, envBytes) {
		return fmt.Errorf("send policy to device %s failed", deviceID)
	}

	// Update state to 'pending'
	priorSettings := compiled.Settings
	if prior, err := h.repo.GetDeviceState(ctx, deviceID); err == nil && prior != nil {
		priorSettings = prior.AppliedSettings
	}
	state := &DeviceSystemPolicyState{
		DeviceID:        deviceID,
		PolicyVersion:   compiled.Version,
		Status:          "pending",
		AppliedSettings: priorSettings,
	}
	_ = h.repo.UpdateDeviceState(ctx, state)

	return nil
}

// DevicesRegrouped satisfies devicemgmt.MembershipObserver. A device that joined
// or left a group may now fall under a different set of policies, so it gets its
// recompiled policy now. An offline device is caught on reconnect, because its
// compiled version no longer matches the stored one.
func (h *Handler) DevicesRegrouped(ctx context.Context, deviceIDs []string) {
	for _, id := range deviceIDs {
		if err := h.dispatchDevice(ctx, id); err != nil {
			log.Warn().Err(err).Str("device", id).Msg("re-dispatch system policy after group change failed")
		}
	}
}

var _ devicemgmt.MembershipObserver = (*Handler)(nil)

func (h *Handler) dispatchAffected(ctx context.Context, targetType, targetID string) {
	if h.hub == nil {
		return
	}
	deviceIDs, err := h.repo.GetTargetDeviceIDs(ctx, targetType, targetID)
	if err != nil {
		log.Error().Err(err).Str("target_type", targetType).Str("target_id", targetID).Msg("get target devices failed")
		return
	}

	seen := make(map[string]bool)
	for _, devID := range deviceIDs {
		if seen[devID] {
			continue
		}
		seen[devID] = true
		if err := h.dispatchDevice(ctx, devID); err != nil {
			log.Warn().Err(err).Str("device", devID).Msg("dispatch system policy failed")
		}
	}
}

// SyncOnReconnect satisfies transport.SystemPolicySyncer.
// It checks if the device's recorded policy state matches the compiled policy version.
// If there is a version mismatch, or if state is not 'synced', it automatically pushes the policy.
func (h *Handler) SyncOnReconnect(ctx context.Context, deviceID string) error {
	if h.hub == nil || !h.hub.Online(deviceID) {
		return nil
	}
	compiled, err := h.repo.CompileEffectivePolicy(ctx, deviceID)
	if err != nil {
		return fmt.Errorf("compile policy for %s: %w", deviceID, err)
	}

	state, err := h.repo.GetDeviceState(ctx, deviceID)
	switch {
	case err == nil && state != nil && state.PolicyVersion == compiled.Version && state.Status == "synced":
		return nil // Already up to date and successfully applied
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("read device policy state: %w", err)
	}

	payload, err := systemPolicyApplyEnvelope(compiled)
	if err != nil {
		return fmt.Errorf("encode system policy envelope: %w", err)
	}

	if !h.hub.SendTo(deviceID, payload) {
		return fmt.Errorf("send system policy on reconnect: %s", deviceID)
	}

	priorSettings := compiled.Settings
	if state != nil {
		priorSettings = state.AppliedSettings
	}
	pendingState := &DeviceSystemPolicyState{
		DeviceID:        deviceID,
		PolicyVersion:   compiled.Version,
		Status:          "pending",
		AppliedSettings: priorSettings,
	}
	_ = h.repo.UpdateDeviceState(ctx, pendingState)

	return nil
}

func systemPolicyApplyEnvelope(compiled *CompiledPolicy) ([]byte, error) {
	env := transport.Envelope{
		Type:    transport.TypeCommand,
		ID:      compiled.Version,
		Command: "system_policy.apply",
		Payload: map[string]any{
			"policy_version": compiled.Version,
			"settings":       compiled.Settings,
		},
	}
	return json.Marshal(env)
}

func (h *Handler) wallpaperUpload(w http.ResponseWriter, r *http.Request) {
	// 10MB limit
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "file too large or invalid multipart form")
		return
	}

	file, header, err := r.FormFile("wallpaper")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing wallpaper file field")
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".bmp" {
		writeErr(w, http.StatusBadRequest, "only JPG, PNG, and BMP image files allowed")
		return
	}

	randBytes := make([]byte, 16)
	_, _ = rand.Read(randBytes)
	newFilename := fmt.Sprintf("wallpaper_%s%s", hex.EncodeToString(randBytes), ext)

	uploadDir := h.wallpaperDir
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create wallpaper directory")
		return
	}

	dstPath := filepath.Join(uploadDir, newFilename)
	dst, err := os.Create(dstPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to save wallpaper file")
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to write wallpaper file")
		return
	}

	wallpaperURL := fmt.Sprintf("/api/system-policies/wallpapers/%s", newFilename)
	writeJSON(w, http.StatusOK, map[string]string{
		"wallpaper_url": wallpaperURL,
		"filename":      newFilename,
	})
}

func (h *Handler) serveWallpaper(w http.ResponseWriter, r *http.Request) {
	filename := filepath.Base(chi.URLParam(r, "filename"))
	filePath := filepath.Join(h.wallpaperDir, filename)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filePath)
}

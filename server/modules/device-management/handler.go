package devicemanagement

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/jmoiron/sqlx"
)

// Handler exposes the device-management HTTP API.
type Handler struct {
	repo      *Repository
	inventory *inventoryRepository // Phase 2: filters the device list by group
	db        *sqlx.DB
	jwt       *auth.JWTService
	ttl       time.Duration // enrollment token TTL
}

func NewHandler(repo *Repository, db *sqlx.DB, jwt *auth.JWTService, enrollmentTTL time.Duration) *Handler {
	return &Handler{repo: repo, inventory: newInventoryRepository(db), db: db, jwt: jwt, ttl: enrollmentTTL}
}

// Register mounts routes on the given router. Auth/RBAC middleware must be applied
// by the caller for protected routes (see cmd/server/main.go).
func (h *Handler) Register(r chi.Router) {
	r.Route("/api", func(r chi.Router) {
		r.With(h.jwt.RequireAuth, rbac.RequireRole(rbac.RoleViewer)).Get("/devices", h.listDevices)
		r.With(h.jwt.RequireAuth, rbac.RequireRole(rbac.RoleViewer)).Get("/devices/{id}", h.getDevice)
		r.With(h.jwt.RequireAuth, rbac.RequireRole(rbac.RoleTechnician)).Patch("/devices/{id}", h.updateDevice)
		// Creating enrollment tokens is admin-only.
		r.With(h.jwt.RequireAuth, rbac.RequireRole(rbac.RoleAdmin)).Post("/devices/enroll-token", h.createEnrollToken)
		r.With(h.jwt.RequireAuth, rbac.RequireRole(rbac.RoleAdmin)).Post("/devices/{id}/enroll-token", h.enrollExisting)
		r.With(h.jwt.RequireAuth, rbac.RequireRole(rbac.RoleAdmin)).Post("/devices/{id}/rotate-secret", h.rotateDeviceSecret)
		r.With(h.jwt.RequireAuth, rbac.RequireRole(rbac.RoleAdmin)).Post("/devices/{id}/uninstall-token", h.createUninstallToken)
		r.With(h.jwt.RequireAuth, rbac.RequireRole(rbac.RoleTechnician)).Post("/devices/{id}/verify-uninstall-token", h.verifyUninstallTokenConsole)
		r.With(h.jwt.RequireAuth, rbac.RequireRole(rbac.RoleTechnician)).Get("/audit-logs", h.listAudit)
		r.With(h.jwt.RequireAuth, rbac.RequireRole(rbac.RoleTechnician)).Get("/audit-logs/verify", h.verifyAuditChain)

		// Agent endpoint for uninstall verification
		r.Post("/agent/devices/{id}/verify-uninstall", h.agentVerifyUninstall)
	})
}

// deviceDTO is the API representation of a device. Never leaks secret hashes.
type deviceDTO struct {
	ID           string     `json:"id"`
	Hostname     string     `json:"hostname"`
	OSName       string     `json:"os_name"`
	OSVersion    string     `json:"os_version"`
	AgentVersion string     `json:"agent_version"`
	Status       string     `json:"status"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	Site         string     `json:"site"`
	EnrolledAt   time.Time  `json:"enrolled_at"`
	RetiredAt    *time.Time `json:"retired_at,omitempty"`
	// LastPatchScanAt is when this device last ran a patch scan, or null if it
	// never has. The Patches page needs it to tell an unpatched device from an
	// unexamined one.
	LastPatchScanAt *time.Time `json:"last_patch_scan_at"`
	// Capabilities is decoded from its stored JSON so the console sees an array,
	// not a stringified blob.
	Capabilities []string `json:"capabilities,omitempty"`
}

func toDTO(d Device) deviceDTO {
	var caps []string
	if d.Capabilities != nil && *d.Capabilities != "" {
		_ = json.Unmarshal([]byte(*d.Capabilities), &caps)
	}
	return deviceDTO{
		ID: d.ID, Hostname: d.Hostname, OSName: d.OSName, OSVersion: d.OSVersionString(),
		AgentVersion: d.AgentVersionString(), Status: d.Status, LastSeenAt: d.LastSeenAt,
		Site: d.SiteValue(), EnrolledAt: d.EnrolledAt,
		RetiredAt: d.RetiredAt, LastPatchScanAt: d.LastPatchScanAt, Capabilities: caps,
	}
}

func (h *Handler) listDevices(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	site := r.URL.Query().Get("site")
	groupID := r.URL.Query().Get("group_id")
	limit, offset := pageParams(r)

	// The group filter is inventory-domain knowledge; route it through the
	// inventory repository rather than duplicating membership SQL here.
	if groupID != "" {
		h.listDevicesByGroup(w, r, groupID, limit, offset)
		return
	}

	devices, total, err := h.repo.ListPaged(r.Context(), status, site, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	dtos := make([]deviceDTO, 0, len(devices))
	for _, d := range devices {
		dtos = append(dtos, toDTO(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": dtos, "count": len(dtos), "total": total,
		"limit": limit, "offset": offset,
	})
}

// listDevicesByGroup is the group-filtered variant of listDevices, sharing the
// response shape so the console can use one component for both lists.
func (h *Handler) listDevicesByGroup(w http.ResponseWriter, r *http.Request, groupID string, limit, offset int) {
	total, err := h.inventory.countGroupDevices(r.Context(), groupID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	devices, err := h.inventory.listGroupDevices(r.Context(), groupID, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dtos := make([]deviceDTO, 0, len(devices))
	for _, d := range devices {
		dtos = append(dtos, toDTO(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": dtos, "count": len(dtos), "total": total,
		"limit": limit, "offset": offset,
	})
}

func (h *Handler) getDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	d, err := h.repo.GetByID(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toDTO(d))
}

type createEnrollTokenReq struct {
	Hostname        string `json:"hostname"`
	OSName          string `json:"os_name"`
	Site            string `json:"site"`
	ExpiresInHours  *int   `json:"expires_in_hours,omitempty"`
	DurationSeconds *int64 `json:"duration_seconds,omitempty"`
	MaxUses         *int   `json:"max_uses,omitempty"`
	IsMultiUse      bool   `json:"is_multi_use,omitempty"`
	Name            string `json:"name,omitempty"`
}

type createEnrollTokenResp struct {
	DeviceID        string    `json:"device_id,omitempty"`
	TokenID         string    `json:"token_id,omitempty"`
	EnrollmentToken string    `json:"enrollment_token"`
	ExpiresAt       time.Time `json:"expires_at"`
	MaxUses         int       `json:"max_uses"`
	IsMultiUse      bool      `json:"is_multi_use"`
}

func (h *Handler) issueEnrollmentToken() (plaintext, hash string, expiresAt time.Time) {
	plaintext = GenerateToken()
	hash = HashToken(plaintext) // consumed to NULL once the agent enrolls
	return plaintext, hash, time.Now().UTC().Add(h.ttl)
}

// enrollExisting issues a token for an already known row. It is the re-entry
// path after retire/restore or a lost credential exchange, not a new agent.
func (h *Handler) enrollExisting(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	plain, tokenHash, expiresAt := h.issueEnrollmentToken()
	if err := h.repo.ReissueEnrollmentToken(r.Context(), id, tokenHash, HashToken(GenerateToken()), expiresAt); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "device not found or retired")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	actor := auth.UserIDFromContext(r.Context())
	_ = audit.Log(r.Context(), h.db, "user", actor, "device.enroll_token_reissued", id, nil)
	writeJSON(w, http.StatusOK, createEnrollTokenResp{DeviceID: id, EnrollmentToken: plain, ExpiresAt: expiresAt})
}

// rotateDeviceSecret replaces the device's persistent secret with a new one.
// The old secret immediately stops working for new connections. The new secret
// is returned so the admin can deliver it to the agent (via re-enrollment).
func (h *Handler) rotateDeviceSecret(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	newPlain, err := h.repo.RotateDeviceSecret(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "device not found or retired")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	actor := auth.UserIDFromContext(r.Context())
	_ = audit.Log(r.Context(), h.db, "user", actor, "device.secret_rotated", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{
		"device_id":     id,
		"device_secret": newPlain,
		"message":       "Device secret rotated. The old secret is now invalid. Use the new secret for re-enrollment.",
	})
}

// createEnrollToken pre-registers a device or issues a shared multi-use token.
// The agent exchanges the token for a persistent secret. Only the token hash is stored.
func (h *Handler) createEnrollToken(w http.ResponseWriter, r *http.Request) {
	var req createEnrollTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	plain := GenerateToken()
	now := time.Now().UTC()
	tokenHash := HashToken(plain)

	// Configurable expiration duration
	ttl := h.ttl
	if req.ExpiresInHours != nil && *req.ExpiresInHours > 0 {
		ttl = time.Duration(*req.ExpiresInHours) * time.Hour
	} else if req.DurationSeconds != nil && *req.DurationSeconds > 0 {
		ttl = time.Duration(*req.DurationSeconds) * time.Second
	}
	expiresAt := now.Add(ttl)

	var site *string
	if req.Site != "" {
		site = &req.Site
	}

	actor := auth.UserIDFromContext(r.Context())

	// Multi-Device / Reusable Shared Token
	maxUses := 1
	if req.MaxUses != nil {
		maxUses = *req.MaxUses
	}
	if req.IsMultiUse || (req.MaxUses != nil && *req.MaxUses != 1) {
		tokenID := NewID()
		tokenName := req.Name
		if tokenName == "" {
			tokenName = req.Hostname
			if tokenName == "" {
				tokenName = "Batch Enrollment"
			}
		}
		if err := h.repo.CreateSharedEnrollmentToken(r.Context(), tokenID, tokenHash, tokenName, site, expiresAt, maxUses, actor); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = audit.Log(r.Context(), h.db, "user", actor, "device.enroll_token_created_shared", tokenID, map[string]string{
			"name": tokenName, "max_uses": strconv.Itoa(maxUses), "expires_at": expiresAt.Format(time.RFC3339),
		})
		writeJSON(w, http.StatusCreated, createEnrollTokenResp{
			TokenID:         tokenID,
			EnrollmentToken: plain,
			ExpiresAt:       expiresAt,
			MaxUses:         maxUses,
			IsMultiUse:      true,
		})
		return
	}

	// Single Device Pre-Registration (Legacy / 1-to-1)
	if req.Hostname == "" {
		req.Hostname = "Pending Enrollment"
	}
	if req.OSName == "" {
		req.OSName = OSWindows
	}
	switch req.OSName {
	case OSWindows, OSLinux, OSMacOS:
	default:
		writeErr(w, http.StatusBadRequest, "os_name must be windows, linux or macos")
		return
	}

	dev := Device{
		ID:                       NewID(),
		Hostname:                 req.Hostname,
		OSName:                   req.OSName,
		Status:                   StatusOffline,
		EnrolledAt:               now,
		EnrollmentTokenHash:      &tokenHash,
		EnrollmentTokenExpiresAt: &expiresAt,
		Site:                     site,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	if err := h.repo.Create(r.Context(), dev); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = audit.Log(r.Context(), h.db, "user", actor, "device.enroll_token_created", dev.ID, map[string]string{
		"hostname": req.Hostname, "os_name": req.OSName, "site": req.Site,
	})

	writeJSON(w, http.StatusCreated, createEnrollTokenResp{
		DeviceID:        dev.ID,
		EnrollmentToken: plain,
		ExpiresAt:       expiresAt,
		MaxUses:         1,
		IsMultiUse:      false,
	})
}

type updateDeviceReq struct {
	Hostname string `json:"hostname"`
	Site     string `json:"site"`
}

func (h *Handler) updateDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req updateDeviceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Hostname == "" && req.Site == "" {
		writeErr(w, http.StatusBadRequest, "hostname or site is required")
		return
	}
	if err := h.repo.UpdateDevice(r.Context(), id, req.Hostname, req.Site); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "device not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	actor := auth.UserIDFromContext(r.Context())
	_ = audit.Log(r.Context(), h.db, "user", actor, "device.updated", id, map[string]string{
		"hostname": req.Hostname, "site": req.Site,
	})
	d, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, toDTO(d))
}

func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := audit.List(r.Context(), h.db, 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": entries, "count": len(entries)})
}

func (h *Handler) verifyAuditChain(w http.ResponseWriter, r *http.Request) {
	report, err := audit.VerifyChain(r.Context(), h.db)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = audit.Log(r.Context(), h.db, "user",
		auth.UserIDFromContext(r.Context()), "audit.verify", "",
		map[string]any{"ok": report.OK, "checked": report.Checked})
	writeJSON(w, http.StatusOK, report)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

type uninstallTokenResp struct {
	DeviceID       string    `json:"device_id"`
	UninstallToken string    `json:"uninstall_token"`
	ExpiresAt      time.Time `json:"expires_at"`
}

func (h *Handler) createUninstallToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dev, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "device not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if dev.RetiredAt != nil {
		writeErr(w, http.StatusBadRequest, "device is retired")
		return
	}

	plain := "UNINST-" + GenerateToken()[:16]
	hash := HashToken(plain)
	expiresAt := time.Now().UTC().Add(2 * time.Hour)

	actor := auth.UserIDFromContext(r.Context())
	if err := h.repo.CreateUninstallToken(r.Context(), id, hash, actor, expiresAt); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = audit.Log(r.Context(), h.db, "user", actor, "device.uninstall_token_created", id, nil)
	writeJSON(w, http.StatusOK, uninstallTokenResp{
		DeviceID:       id,
		UninstallToken: plain,
		ExpiresAt:      expiresAt,
	})
}

type verifyTokenReq struct {
	Token string `json:"token"`
}

func (h *Handler) verifyUninstallTokenConsole(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req verifyTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		writeErr(w, http.StatusBadRequest, "missing token")
		return
	}

	valid, err := h.repo.VerifyUninstallToken(r.Context(), id, strings.TrimSpace(req.Token))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": valid})
}

func (h *Handler) agentVerifyUninstall(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	authedID, ok := AuthenticateAgent(w, r, h.repo)
	if !ok {
		return
	}
	if authedID != id {
		writeErr(w, http.StatusForbidden, "device ID mismatch")
		return
	}

	var req verifyTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		writeErr(w, http.StatusBadRequest, "missing token in request body")
		return
	}

	valid, err := h.repo.VerifyAndConsumeUninstallToken(r.Context(), id, strings.TrimSpace(req.Token))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !valid {
		writeErr(w, http.StatusForbidden, "invalid or expired uninstallation verification token")
		return
	}

	_ = audit.Log(r.Context(), h.db, "agent", id, "device.uninstall_verified", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"valid":   true,
		"message": "uninstallation authorized",
	})
}

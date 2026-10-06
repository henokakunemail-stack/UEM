package devicemanagement

import (
	"encoding/json"
	"errors"
	"net/http"
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
		// Creating enrollment tokens is admin-only.
		r.With(h.jwt.RequireAuth, rbac.RequireRole(rbac.RoleAdmin)).Post("/devices/enroll-token", h.createEnrollToken)
		r.With(h.jwt.RequireAuth, rbac.RequireRole(rbac.RoleTechnician)).Get("/audit-logs", h.listAudit)
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
	Hostname string `json:"hostname"`
	OSName   string `json:"os_name"`
	Site     string `json:"site"`
}

type createEnrollTokenResp struct {
	DeviceID        string    `json:"device_id"`
	EnrollmentToken string    `json:"enrollment_token"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// createEnrollToken pre-registers a device and returns a one-time enrollment token
// the agent exchanges for a persistent secret. Only the token hash is stored.
func (h *Handler) createEnrollToken(w http.ResponseWriter, r *http.Request) {
	var req createEnrollTokenReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Hostname == "" || req.OSName == "" {
		writeErr(w, http.StatusBadRequest, "hostname and os_name are required")
		return
	}
	switch req.OSName {
	case OSWindows, OSLinux, OSMacOS:
	default:
		writeErr(w, http.StatusBadRequest, "os_name must be windows, linux or macos")
		return
	}

	plain := GenerateToken()
	now := time.Now().UTC()
	tokenHash := HashToken(plain) // consumed to NULL once the agent enrolls
	expiresAt := now.Add(h.ttl)
	var site *string
	if req.Site != "" {
		site = &req.Site
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

	actor := auth.UserIDFromContext(r.Context())
	_ = audit.Log(r.Context(), h.db, "user", actor, "device.enroll_token_created", dev.ID, map[string]string{
		"hostname": req.Hostname, "os_name": req.OSName, "site": req.Site,
	})

	writeJSON(w, http.StatusCreated, createEnrollTokenResp{
		DeviceID:        dev.ID,
		EnrollmentToken: plain,
		ExpiresAt:       expiresAt,
	})
}

func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := audit.List(r.Context(), h.db, 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": entries, "count": len(entries)})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

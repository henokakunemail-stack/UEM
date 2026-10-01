package assetlicense

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type Auditor interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

type Handler struct {
	repo    *Repository
	auditor Auditor
	authMw  func(http.Handler) http.Handler
}

func NewHandler(repo *Repository, auditor Auditor, authMw func(http.Handler) http.Handler) *Handler {
	return &Handler{
		repo:    repo,
		auditor: auditor,
		authMw:  authMw,
	}
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(cr chi.Router) {
		cr.Use(h.authMw)

		// Assets
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/assets/summary", h.handleGetAssetSummary)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/assets", h.handleListAssets)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/assets/{id}", h.handleGetAsset)
		cr.With(rbac.RequireRole(rbac.RoleTechnician)).Post("/api/assets", h.handleCreateAsset)
		cr.With(rbac.RequireRole(rbac.RoleTechnician)).Put("/api/assets/{id}", h.handleUpdateAsset)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Delete("/api/assets/{id}", h.handleDeleteAsset)

		// Licenses & Compliance
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/licenses/compliance", h.handleGetCompliance)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/licenses", h.handleListLicenses)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/licenses/{id}", h.handleGetLicense)
		cr.With(rbac.RequireRole(rbac.RoleAdmin)).Post("/api/licenses", h.handleCreateLicense)
		cr.With(rbac.RequireRole(rbac.RoleTechnician)).Post("/api/licenses/{id}/allocate", h.handleAllocateLicense)
		cr.With(rbac.RequireRole(rbac.RoleTechnician)).Post("/api/licenses/{id}/deallocate", h.handleDeallocateLicense)
	})
}

func (h *Handler) handleGetAssetSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := h.repo.GetAssetSummary(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) handleListAssets(w http.ResponseWriter, r *http.Request) {
	site := r.URL.Query().Get("site")
	status := r.URL.Query().Get("status")
	list, err := h.repo.ListAssets(r.Context(), site, status)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	a, err := h.repo.GetAsset(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "asset not found"})
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *Handler) handleCreateAsset(w http.ResponseWriter, r *http.Request) {
	var a HardwareAsset
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if a.AssetTag == "" || a.ModelName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "asset_tag and model_name are required"})
		return
	}

	if err := h.repo.CreateAsset(r.Context(), &a); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "asset.create", a.ID, map[string]string{
		"asset_tag":  a.AssetTag,
		"model_name": a.ModelName,
	})

	writeJSON(w, http.StatusCreated, a)
}

func (h *Handler) handleUpdateAsset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := h.repo.GetAsset(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "asset not found"})
		return
	}

	var updateReq HardwareAsset
	if err := json.NewDecoder(r.Body).Decode(&updateReq); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	existing.AssetTag = updateReq.AssetTag
	existing.DeviceID = updateReq.DeviceID
	existing.ModelName = updateReq.ModelName
	existing.SerialNumber = updateReq.SerialNumber
	existing.Vendor = updateReq.Vendor
	existing.Site = updateReq.Site
	existing.Department = updateReq.Department
	existing.AssignedUser = updateReq.AssignedUser
	existing.PurchaseDate = updateReq.PurchaseDate
	existing.PurchaseCost = updateReq.PurchaseCost
	existing.WarrantyExpiresAt = updateReq.WarrantyExpiresAt
	existing.Status = updateReq.Status
	existing.Notes = updateReq.Notes

	if err := h.repo.UpdateAsset(r.Context(), existing); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "asset.update", id, map[string]string{
		"asset_tag": existing.AssetTag,
		"status":    existing.Status,
	})

	writeJSON(w, http.StatusOK, existing)
}

func (h *Handler) handleDeleteAsset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.DeleteAsset(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "asset.delete", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) handleListLicenses(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.ListLicenses(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for i := range list {
		redactLicenseKey(list[i], r)
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) handleGetLicense(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	l, err := h.repo.GetLicense(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "license not found"})
		return
	}
	redactLicenseKey(l, r)
	writeJSON(w, http.StatusOK, l)
}

// redactLicenseKey blanks the product key on the way out to a viewer.
//
// Both read routes are RoleViewer (handler.go:48-49) and both serialise
// SoftwareLicense as-is, so any viewer could collect every product key in the
// estate and try them against the vendor's portal. That is a redeemable
// credential, not a field the console reads: AssetLicensePage lists software
// title, publisher, type, seats and compliance, and never sends license_key on
// create. Nothing in the UI needs it.
//
// Admin keeps the real value, so the technician who manages the estate can
// still copy it out of the detail view.
//
// ponytail: a dedicated DTO would be the shape if the console ever started
// editing a key in place. Until then this is one assignment.
func redactLicenseKey(l *SoftwareLicense, r *http.Request) {
	if rbac.RoleFromContext(r.Context()) == rbac.RoleAdmin {
		return
	}
	l.LicenseKey = ""
}

func (h *Handler) handleCreateLicense(w http.ResponseWriter, r *http.Request) {
	var l SoftwareLicense
	if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if l.SoftwareName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "software_name is required"})
		return
	}

	if err := h.repo.CreateLicense(r.Context(), &l); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "license.create", l.ID, map[string]string{
		"software_name": l.SoftwareName,
		"seats":         strconv.Itoa(l.TotalSeats),
	})

	writeJSON(w, http.StatusCreated, l)
}

func (h *Handler) handleAllocateLicense(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.DeviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "device_id is required"})
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	if err := h.repo.AllocateLicense(r.Context(), id, req.DeviceID, actorID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	_ = h.auditor.Log(r.Context(), "user", actorID, "license.allocate", id, map[string]string{
		"device_id": req.DeviceID,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "allocated"})
}

func (h *Handler) handleDeallocateLicense(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	if err := h.repo.DeallocateLicense(r.Context(), id, req.DeviceID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.auditor.Log(r.Context(), "user", actorID, "license.deallocate", id, map[string]string{
		"device_id": req.DeviceID,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "deallocated"})
}

func (h *Handler) handleGetCompliance(w http.ResponseWriter, r *http.Request) {
	compliance, err := h.repo.ComputeCompliance(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"audited_at": time.Now().UTC(),
		"compliance": compliance,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

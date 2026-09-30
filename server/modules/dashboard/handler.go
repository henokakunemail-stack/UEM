package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

// Handler serves the dashboard metrics endpoints.
type Handler struct {
	repo   *Repository
	authMW func(http.Handler) http.Handler
}

// NewHandler constructs a Handler.
func NewHandler(repo *Repository, authMW func(http.Handler) http.Handler) *Handler {
	if authMW == nil {
		authMW = func(next http.Handler) http.Handler { return next }
	}
	return &Handler{repo: repo, authMW: authMW}
}

// Register mounts the dashboard routes on the router.
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authMW)
		r.Use(rbac.RequireRole(rbac.RoleViewer))

		r.Get("/api/dashboard/summary", h.getSummary)
		r.Get("/api/dashboard/sites", h.getSites)
		r.Get("/api/dashboard/os", h.getOS)
		r.Get("/api/dashboard/alerts", h.getAlerts)
		r.Get("/api/dashboard/activity", h.getActivity)
	})
}

func (h *Handler) getSummary(w http.ResponseWriter, r *http.Request) {
	s, err := h.repo.GetSummary(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) getSites(w http.ResponseWriter, r *http.Request) {
	sites, err := h.repo.GetSiteMetrics(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sites)
}

func (h *Handler) getOS(w http.ResponseWriter, r *http.Request) {
	osMetrics, err := h.repo.GetOSMetrics(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, osMetrics)
}

func (h *Handler) getAlerts(w http.ResponseWriter, r *http.Request) {
	alerts, err := h.repo.GetAlerts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, alerts)
}

func (h *Handler) getActivity(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}
	activity, err := h.repo.GetRecentActivity(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, activity)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

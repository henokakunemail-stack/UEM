package compliance

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/httpguard"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

// Handler serves the compliance API.
type Handler struct {
	gen            *Generator
	authMiddleware func(http.Handler) http.Handler
}

// NewHandler wires the API to a generator.
func NewHandler(gen *Generator, authMiddleware func(http.Handler) http.Handler) *Handler {
	return &Handler{gen: gen, authMiddleware: authMiddleware}
}

// Register mounts the routes.
func (h *Handler) Register(r chi.Router) {
	viewer := r.With(h.authMiddleware)
	admin := r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin))
	tech := r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician))

	viewer.Get("/api/compliance/profiles", h.listProfiles)
	admin.Post("/api/compliance/profiles", h.createProfile)
	admin.Put("/api/compliance/profiles/{id}", h.updateProfile)
	admin.Delete("/api/compliance/profiles/{id}", h.deleteProfile)

	viewer.Get("/api/compliance/evaluation", h.evaluation)

	viewer.Get("/api/compliance/reports", h.listReports)
	tech.Post("/api/compliance/reports", h.generate)
	viewer.Get("/api/compliance/reports/{id}/pdf", h.download)
	viewer.Post("/api/compliance/reports/verify", h.verify)

	viewer.Get("/api/compliance/frameworks", h.listFrameworks)
	viewer.Get("/api/compliance/frameworks/{key}/assessment", h.frameworkAssessment)
	tech.Post("/api/compliance/frameworks/reports", h.generateFramework)
}

// frameworkInfo is one template as listed in the console.
type frameworkInfo struct {
	ID             string   `json:"id"`
	Family         string   `json:"family"`
	Name           string   `json:"name"`
	Regulation     string   `json:"regulation"`
	EffectiveFrom  string   `json:"effective_from,omitempty"`
	EffectiveUntil string   `json:"effective_until,omitempty"`
	ReviewedAt     string   `json:"reviewed_at"`
	Origin         string   `json:"origin"`
	File           string   `json:"file,omitempty"`
	Controls       int      `json:"controls"`
	Mapped         int      `json:"mapped"`
	Pending        []string `json:"pending,omitempty"`
}

func (h *Handler) listFrameworks(w http.ResponseWriter, r *http.Request) {
	ts, errs := h.gen.LoadTemplates()
	out := make([]frameworkInfo, 0, len(ts))
	for _, t := range ts {
		mapped := 0
		for _, c := range t.Controls {
			if len(c.Evidence) > 0 {
				mapped++
			}
		}
		out = append(out, frameworkInfo{ID: t.ID, Family: t.Family, Name: t.Name, Regulation: t.Regulation,
			EffectiveFrom: t.EffectiveFrom, EffectiveUntil: t.EffectiveUntil, ReviewedAt: t.ReviewedAt,
			Origin: t.Origin, File: t.File, Controls: len(t.Controls), Mapped: mapped, Pending: t.Pending})
	}
	if errs == nil {
		errs = []TemplateError{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"frameworks": out, "errors": errs, "template_dir": h.gen.TemplateDir()})
}

func frameworkErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "framework or profile not found")
	case strings.HasPrefix(err.Error(), "period") || strings.HasPrefix(err.Error(), "no version"):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (h *Handler) frameworkAssessment(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	snap, err := h.gen.AssessFramework(r.Context(), chi.URLParam(r, "key"), q.Get("profile_id"), q.Get("period"))
	if err != nil {
		frameworkErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (h *Handler) generateFramework(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Framework string `json:"framework"`
		ProfileID string `json:"profile_id"`
		Period    string `json:"period"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Framework == "" {
		writeErr(w, http.StatusBadRequest, "framework is required")
		return
	}
	userID := auth.UserIDFromContext(r.Context())
	rep, err := h.gen.GenerateFramework(r.Context(), FrameworkRequest{
		Framework: req.Framework, ProfileID: req.ProfileID, Period: req.Period,
		ActorType: "user", ActorID: userID, ActorName: h.gen.Repo.Username(r.Context(), userID),
	})
	if err != nil {
		frameworkErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rep)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (h *Handler) audit(r *http.Request, action, target string, details map[string]string) {
	_ = h.gen.Audit.Log(r.Context(), "user", auth.UserIDFromContext(r.Context()), action, target, details)
}

func (h *Handler) listProfiles(w http.ResponseWriter, r *http.Request) {
	ps, err := h.gen.Repo.ListProfiles(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

func decodeProfile(w http.ResponseWriter, r *http.Request) (Profile, bool) {
	var p Profile
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return p, false
	}
	return p, true
}

func (h *Handler) createProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := decodeProfile(w, r)
	if !ok {
		return
	}
	created, err := h.gen.Repo.CreateProfile(r.Context(), p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "compliance.profile.create", created.ID, map[string]string{"name": created.Name})
	writeJSON(w, http.StatusCreated, created)
}

func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := decodeProfile(w, r)
	if !ok {
		return
	}
	p.ID = chi.URLParam(r, "id")
	updated, err := h.gen.Repo.UpdateProfile(r.Context(), p)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "profile not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rules, _ := json.Marshal(updated.Rules)
	h.audit(r, "compliance.profile.update", updated.ID, map[string]string{"name": updated.Name, "rules": string(rules)})
	writeJSON(w, http.StatusOK, updated)
}

func (h *Handler) deleteProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	err := h.gen.Repo.DeleteProfile(r.Context(), id)
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "profile not found")
		return
	case errors.Is(err, ErrDefaultProfile):
		writeErr(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "compliance.profile.delete", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// evaluationResponse is the live view the console renders.
type evaluationResponse struct {
	Profile     Profile        `json:"profile"`
	Scope       string         `json:"scope"`
	EvaluatedAt time.Time      `json:"evaluated_at"`
	Summary     FleetSummary   `json:"summary"`
	Devices     []DeviceResult `json:"devices"`
}

func (h *Handler) evaluation(w http.ResponseWriter, r *http.Request) {
	p, err := h.gen.Repo.GetProfile(r.Context(), r.URL.Query().Get("profile_id"))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "profile not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := h.gen.now()
	results, err := h.gen.Repo.Evaluate(r.Context(), p, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, evaluationResponse{
		Profile: p, Scope: h.gen.scopeText(r.Context(), p), EvaluatedAt: now,
		Summary: Summarize(p.Rules, results), Devices: results,
	})
}

func (h *Handler) listReports(w http.ResponseWriter, r *http.Request) {
	reps, err := h.gen.Repo.ListReports(r.Context(), 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, reps)
}

func (h *Handler) generate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProfileID string `json:"profile_id"`
		Period    string `json:"period"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	userID := auth.UserIDFromContext(r.Context())
	rep, err := h.gen.Generate(r.Context(), Request{
		ProfileID: req.ProfileID, Kind: KindManual, Period: req.Period,
		ActorType: "user", ActorID: userID, ActorName: h.gen.Repo.Username(r.Context(), userID),
	})
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "profile not found")
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if strings.HasPrefix(err.Error(), "period") {
			status = http.StatusBadRequest
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rep)
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	rep, err := h.gen.Repo.GetReport(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "report not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	f, err := os.Open(h.gen.ReportPath(rep.ID))
	if err != nil {
		writeErr(w, http.StatusNotFound, "the report file is missing from the server's data directory")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="laporan-kepatuhan-`+rep.ID+`.pdf"`)
	// The archived hash travels with the download so a client can compare it
	// with the bytes it received.
	w.Header().Set("X-Report-SHA256", rep.FileSHA256)
	http.ServeContent(httpguard.LongTransfer(w, httpguard.TransferIdleTimeout), r, rep.ID+".pdf", st.ModTime(), f)
}

var sha256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// verify looks a file hash up in the archive. The console hashes the file in
// the browser, so the PDF itself is never uploaded.
func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SHA256 string `json:"sha256"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	sum := strings.TrimSpace(req.SHA256)
	if !sha256Hex.MatchString(sum) {
		writeErr(w, http.StatusBadRequest, "sha256 must be 64 hexadecimal characters")
		return
	}
	rep, err := h.gen.Repo.FindByFileHash(r.Context(), sum)
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"match": false})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"match": true, "report": rep})
}

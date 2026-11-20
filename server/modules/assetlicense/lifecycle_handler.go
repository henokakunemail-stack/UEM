package assetlicense

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
)

// username resolves a user id to a name for the report cover, falling back
// to the id.
func (r *Repository) username(ctx context.Context, id string) string {
	var name string
	if err := r.db.GetContext(ctx, &name, `SELECT username FROM users WHERE id = ?`, id); err != nil || name == "" {
		return id
	}
	return name
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) handleLifecycle(w http.ResponseWriter, r *http.Request) {
	rep, err := h.repo.Lifecycle(r.Context(), h.now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *Handler) handleGetLifecycleSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.repo.GetLifecycleSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) handlePutLifecycleSettings(w http.ResponseWriter, r *http.Request) {
	var s LifecycleSettings
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if err := s.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	actorID := auth.UserIDFromContext(r.Context())
	if err := h.repo.SaveLifecycleSettings(r.Context(), s, actorID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	details := map[string]string{
		"min_ram_gb":            strconv.FormatFloat(s.MinRAMGB, 'f', -1, 64),
		"min_disk_free_percent": strconv.FormatFloat(s.MinDiskFreePercent, 'f', -1, 64),
	}
	for _, c := range Categories {
		v := s.Categories[c]
		details[c+"_life_years"] = strconv.Itoa(v.UsefulLifeYears)
		details[c+"_replacement_cost"] = strconv.FormatFloat(v.ReplacementCost, 'f', -1, 64)
	}
	_ = h.auditor.Log(r.Context(), "user", actorID, "asset.lifecycle.settings_update", lifecycleSettingsID, details)
	writeJSON(w, http.StatusOK, s)
}

// handleLifecycleReport renders the management summary as a PDF download.
// It is not archived: it is the dashboard on paper. Its file hash goes to the
// audit log, so a copy can still be traced to the request that made it.
func (h *Handler) handleLifecycleReport(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	rep, err := h.repo.Lifecycle(r.Context(), now)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	actorID := auth.UserIDFromContext(r.Context())
	id := LifecycleReportID(now)
	pdf, err := RenderLifecyclePDF(rep, id, h.Organization, h.repo.username(r.Context(), actorID))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "render report: " + err.Error()})
		return
	}
	sum := sha256.Sum256(pdf)
	fileHash := hex.EncodeToString(sum[:])
	if err := h.auditor.Log(r.Context(), "user", actorID, "asset.lifecycle.report", id, map[string]string{
		"file_sha256": fileHash,
		"units":       strconv.Itoa(len(rep.Units)),
		"overdue":     strconv.Itoa(rep.Overdue),
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "record report in audit log: " + err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="ringkasan-aset-`+id+`.pdf"`)
	w.Header().Set("X-Report-SHA256", fileHash)
	http.ServeContent(w, r, id+".pdf", now, bytes.NewReader(pdf))
}

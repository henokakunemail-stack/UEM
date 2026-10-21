package alerting

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type AuditLogger interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

type Handler struct {
	repo           *Repository
	evaluator      *Evaluator
	audit          AuditLogger
	authMiddleware func(http.Handler) http.Handler
}

func NewHandler(repo *Repository, evaluator *Evaluator, audit AuditLogger, authMiddleware func(http.Handler) http.Handler) *Handler {
	return &Handler{
		repo:           repo,
		evaluator:      evaluator,
		audit:          audit,
		authMiddleware: authMiddleware,
	}
}

func (h *Handler) Register(r chi.Router) {
	// Alert Rules
	r.With(h.authMiddleware).Get("/api/alerts/rules", h.listRules)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).Post("/api/alerts/rules", h.createRule)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).Get("/api/alerts/rules/{id}", h.getRule)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).Put("/api/alerts/rules/{id}", h.updateRule)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).Delete("/api/alerts/rules/{id}", h.deleteRule)

	// Alert Incidents
	r.With(h.authMiddleware).Get("/api/alerts/incidents", h.listIncidents)
	r.With(h.authMiddleware).Get("/api/alerts/incidents/counts", h.countIncidents)
	r.With(h.authMiddleware).Get("/api/alerts/incidents/{id}", h.getIncident)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).Post("/api/alerts/incidents/{id}/acknowledge", h.acknowledgeIncident)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).Post("/api/alerts/incidents/{id}/resolve", h.resolveIncident)

	// Trigger on-demand evaluation
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).Post("/api/alerts/evaluate", h.triggerEvaluation)
}

func (h *Handler) listRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.repo.ListRules(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rules == nil {
		rules = []AlertRule{}
	}
	writeJSON(w, http.StatusOK, rules)
}

type createRuleReq struct {
	Name         string  `json:"name"`
	RuleType     string  `json:"rule_type"`
	ThresholdVal float64 `json:"threshold_val"`
	Severity     string  `json:"severity"`
	WebhookURL   string  `json:"webhook_url"`
	// A pointer so an omitted flag means enabled, like the column default. A
	// plain bool decoded a missing key as false, and a new rule was off.
	IsEnabled *bool `json:"is_enabled"`
}

// ruleProblem is why a rule cannot be saved: a code the console translates
// and a message for API callers.
type ruleProblem struct{ code, msg string }

// validateRule checks what the evaluator relies on. An unknown rule type was
// stored and counted as evaluated while doing nothing; a threshold out of
// range made a rule that could never fire, or fired on everything.
func validateRule(rule *AlertRule) *ruleProblem {
	if strings.TrimSpace(rule.Name) == "" {
		return &ruleProblem{"invalid_name", "name is required"}
	}
	switch rule.RuleType {
	case "disk_low":
		if rule.ThresholdVal < 1 || rule.ThresholdVal > 99 {
			return &ruleProblem{"invalid_threshold", "disk_low threshold is a free-space percentage from 1 to 99"}
		}
	case "device_offline":
		if rule.ThresholdVal < 1 || rule.ThresholdVal > 10080 {
			return &ruleProblem{"invalid_threshold", "device_offline threshold is minutes offline, from 1 to 10080 (7 days)"}
		}
	case "critical_patch":
		if rule.ThresholdVal < 1 || rule.ThresholdVal != float64(int(rule.ThresholdVal)) {
			return &ruleProblem{"invalid_threshold", "critical_patch threshold is a whole number of missing patches, at least 1"}
		}
	default:
		return &ruleProblem{"invalid_rule_type", "rule_type must be disk_low, device_offline or critical_patch"}
	}
	switch rule.Severity {
	case "info", "warning", "critical":
	default:
		return &ruleProblem{"invalid_severity", "severity must be info, warning or critical"}
	}
	if rule.WebhookURL != "" {
		// The delivery client refuses private and loopback addresses at dial
		// time; this only rejects what can never be delivered at all.
		u, err := url.Parse(rule.WebhookURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return &ruleProblem{"invalid_webhook_url", "webhook_url must be an http or https URL"}
		}
	}
	return nil
}

func (h *Handler) createRule(w http.ResponseWriter, r *http.Request) {
	var req createRuleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	userID := auth.UserIDFromContext(r.Context())
	enabled := req.IsEnabled == nil || *req.IsEnabled
	rule := &AlertRule{
		Name:         req.Name,
		RuleType:     req.RuleType,
		ThresholdVal: req.ThresholdVal,
		Severity:     req.Severity,
		WebhookURL:   req.WebhookURL,
		IsEnabled:    enabled,
		CreatedBy:    userID,
	}
	if p := validateRule(rule); p != nil {
		writeErrCode(w, http.StatusBadRequest, p.code, p.msg)
		return
	}
	if err := h.repo.CreateRule(r.Context(), rule); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.audit.Log(r.Context(), "user", userID, "alert_rule.create", rule.ID, map[string]string{
		"name":      rule.Name,
		"rule_type": rule.RuleType,
		"severity":  rule.Severity,
	})

	writeJSON(w, http.StatusCreated, rule)
}

func (h *Handler) getRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rule, err := h.repo.GetRuleByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "rule not found")
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

// updateRuleReq mirrors createRuleReq but every field is a pointer. JSON
// decoding cannot tell "omitted" from "sent as the zero value", so a plain
// value struct would let {"is_enabled":false} blank the rule_type, severity
// and threshold too. A nil field means the client did not send it.
type updateRuleReq struct {
	Name         *string  `json:"name"`
	RuleType     *string  `json:"rule_type"`
	ThresholdVal *float64 `json:"threshold_val"`
	Severity     *string  `json:"severity"`
	WebhookURL   *string  `json:"webhook_url"`
	IsEnabled    *bool    `json:"is_enabled"`
}

func (h *Handler) updateRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req updateRuleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	rule, err := h.repo.GetRuleByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "rule not found")
		return
	}

	if req.Name != nil {
		rule.Name = *req.Name
	}
	if req.RuleType != nil {
		rule.RuleType = *req.RuleType
	}
	if req.ThresholdVal != nil {
		rule.ThresholdVal = *req.ThresholdVal
	}
	if req.Severity != nil {
		rule.Severity = *req.Severity
	}
	if req.WebhookURL != nil {
		rule.WebhookURL = *req.WebhookURL
	}
	wasEnabled := rule.IsEnabled
	if req.IsEnabled != nil {
		rule.IsEnabled = *req.IsEnabled
	}
	if p := validateRule(rule); p != nil {
		writeErrCode(w, http.StatusBadRequest, p.code, p.msg)
		return
	}

	if err := h.repo.UpdateRule(r.Context(), rule); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// A disabled rule is no longer watching, so its incidents cannot clear
	// on their own any more; they are closed now rather than left open.
	if wasEnabled && !rule.IsEnabled && h.evaluator != nil {
		h.evaluator.ResolveRule(r.Context(), rule, "rule_disabled")
	}

	userID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", userID, "alert_rule.update", rule.ID, map[string]string{
		"name": rule.Name,
	})

	writeJSON(w, http.StatusOK, rule)
}

func (h *Handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.DeleteRule(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}

	userID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", userID, "alert_rule.delete", id, nil)

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) listIncidents(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	severity := r.URL.Query().Get("severity")
	limitStr := r.URL.Query().Get("limit")

	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}

	incidents, err := h.repo.ListIncidents(r.Context(), status, severity, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if incidents == nil {
		incidents = []AlertIncident{}
	}
	writeJSON(w, http.StatusOK, incidents)
}

func (h *Handler) getIncident(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	incident, err := h.repo.GetIncidentByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "incident not found")
		return
	}
	writeJSON(w, http.StatusOK, incident)
}

func (h *Handler) acknowledgeIncident(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	if err := h.repo.AcknowledgeIncident(r.Context(), id, userID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	_ = h.audit.Log(r.Context(), "user", userID, "alert.acknowledge", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "acknowledged"})
}

func (h *Handler) resolveIncident(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	if err := h.repo.ResolveIncident(r.Context(), id, userID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	_ = h.audit.Log(r.Context(), "user", userID, "alert.resolve", id, nil)
	if h.evaluator != nil {
		if inc, err := h.repo.GetIncidentByID(r.Context(), id); err == nil {
			if rule, err := h.repo.GetRuleByID(r.Context(), inc.RuleID); err == nil {
				h.evaluator.DispatchResolved(rule, inc, "resolved_by_operator")
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

func (h *Handler) triggerEvaluation(w http.ResponseWriter, r *http.Request) {
	res, err := h.evaluator.EvaluateAll(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("evaluation error: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// countIncidents answers how many incidents are open and acknowledged. The
// console's cards used to be the length of a list the server caps at 100.
func (h *Handler) countIncidents(w http.ResponseWriter, r *http.Request) {
	c, err := h.repo.CountIncidents(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// writeErrCode adds a machine-readable code the console translates.
func writeErrCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

package networkfilter

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

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
	}
}

func (h *Handler) Register(r chi.Router) {
	// Policy CRUD
	r.With(h.authMiddleware).Get("/api/filter/policies", h.listPolicies)
	r.With(h.authMiddleware).Get("/api/filter/policies/{id}", h.getPolicy)

	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).
		Post("/api/filter/policies", h.createPolicy)

	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).
		Put("/api/filter/policies/{id}", h.updatePolicy)

	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).
		Delete("/api/filter/policies/{id}", h.deletePolicy)

	// Rule CRUD
	r.With(h.authMiddleware).Get("/api/filter/policies/{id}/rules", h.listRules)

	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).
		Post("/api/filter/policies/{id}/rules", h.addRule)

	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).
		Delete("/api/filter/rules/{ruleId}", h.deleteRule)

	// Device Filter Sync & Status
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).
		Post("/api/devices/{id}/filter/sync", h.syncDeviceFilter)

	r.With(h.authMiddleware).
		Get("/api/devices/{id}/filter/state", h.getDeviceFilterState)

	// Agent callback (unauthenticated or device-level auth)
	r.Post("/api/agent/devices/{id}/filter/report", h.agentReportFilterState)
}

func (h *Handler) listPolicies(w http.ResponseWriter, r *http.Request) {
	policies, err := h.repo.ListPolicies(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if policies == nil {
		policies = []FilterPolicy{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": policies, "count": len(policies)})
}

func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	policy, err := h.repo.GetPolicyByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "policy not found")
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

type createPolicyReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	TargetType  string `json:"target_type"`
	TargetID    string `json:"target_id"`
	IsEnabled   *bool  `json:"is_enabled"`
	Priority    int    `json:"priority"`
}

func (h *Handler) createPolicy(w http.ResponseWriter, r *http.Request) {
	var req createPolicyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.TargetType == "" {
		req.TargetType = "all"
	}
	if req.Priority <= 0 {
		req.Priority = 100
	}
	isEnabled := true
	if req.IsEnabled != nil {
		isEnabled = *req.IsEnabled
	}

	actorID := auth.UserIDFromContext(r.Context())
	policy := &FilterPolicy{
		Name:        req.Name,
		Description: req.Description,
		TargetType:  req.TargetType,
		TargetID:    req.TargetID,
		IsEnabled:   isEnabled,
		Priority:    req.Priority,
		CreatedBy:   actorID,
	}

	if err := h.repo.CreatePolicy(r.Context(), policy); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.audit.Log(r.Context(), "user", actorID, "filter.policy_create", policy.ID, map[string]string{
		"name": policy.Name, "target": policy.TargetType,
	})

	writeJSON(w, http.StatusCreated, policy)
}

func (h *Handler) updatePolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	policy, err := h.repo.GetPolicyByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "policy not found")
		return
	}

	var req createPolicyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != "" {
		policy.Name = req.Name
	}
	policy.Description = req.Description
	if req.TargetType != "" {
		policy.TargetType = req.TargetType
	}
	policy.TargetID = req.TargetID
	if req.IsEnabled != nil {
		policy.IsEnabled = *req.IsEnabled
	}
	if req.Priority > 0 {
		policy.Priority = req.Priority
	}

	if err := h.repo.UpdatePolicy(r.Context(), policy); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", actorID, "filter.policy_update", policy.ID, map[string]string{
		"name": policy.Name,
	})

	writeJSON(w, http.StatusOK, policy)
}

func (h *Handler) deletePolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.DeletePolicy(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", actorID, "filter.policy_delete", id, nil)

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) listRules(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rules, err := h.repo.ListRulesByPolicy(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rules == nil {
		rules = []FilterRule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules, "count": len(rules)})
}

type addRuleReq struct {
	RuleType string `json:"rule_type"`
	Pattern  string `json:"pattern"`
	Action   string `json:"action"`
	Category string `json:"category"`
}

func (h *Handler) addRule(w http.ResponseWriter, r *http.Request) {
	policyID := chi.URLParam(r, "id")
	if _, err := h.repo.GetPolicyByID(r.Context(), policyID); err != nil {
		writeErr(w, http.StatusNotFound, "policy not found")
		return
	}

	var req addRuleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Pattern == "" {
		writeErr(w, http.StatusBadRequest, "pattern is required")
		return
	}

	rule := &FilterRule{
		PolicyID: policyID,
		RuleType: req.RuleType,
		Pattern:  req.Pattern,
		Action:   req.Action,
		Category: req.Category,
	}

	if err := h.repo.AddRule(r.Context(), rule); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", actorID, "filter.rule_add", rule.ID, map[string]string{
		"policy_id": policyID, "pattern": rule.Pattern,
	})

	writeJSON(w, http.StatusCreated, rule)
}

func (h *Handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	ruleID := chi.URLParam(r, "ruleId")
	if err := h.repo.DeleteRule(r.Context(), ruleID); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", actorID, "filter.rule_delete", ruleID, nil)

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) syncDeviceFilter(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if _, err := h.devices.GetByID(r.Context(), deviceID); err != nil {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}

	patterns, version, err := h.repo.CompileEffectiveRules(r.Context(), deviceID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "compile rules: "+err.Error())
		return
	}

	// Dispatch command via WebSocket
	cmdPayload := map[string]any{
		"policy_version": version,
		"blocked_domains": patterns,
	}
	env := transport.Envelope{
		Type:    transport.TypeCommand,
		ID:      version,
		Command: "filter.apply",
		Payload: cmdPayload,
	}
	envBytes, _ := json.Marshal(env)

	dispatched := false
	if h.hub != nil && h.hub.Online(deviceID) {
		dispatched = h.hub.SendTo(deviceID, envBytes)
	}

	status := "pending"
	if dispatched {
		status = "dispatched"
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", actorID, "filter.sync_dispatched", deviceID, map[string]string{
		// strconv, not string(rune(...)): the rune conversion turns a count of
		// 12 into a control character rather than the text "12".
		"version": version, "rule_count": strconv.Itoa(len(patterns)),
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":          status,
		"policy_version":  version,
		"effective_rules": len(patterns),
	})
}

func (h *Handler) getDeviceFilterState(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	state, err := h.repo.GetDeviceFilterState(r.Context(), deviceID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"device_id":      deviceID,
			"status":         "pending",
			"rules_applied":  0,
			"policy_version": "none",
		})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

type agentFilterReportReq struct {
	PolicyVersion string `json:"policy_version"`
	Status        string `json:"status"`
	RulesApplied  int    `json:"rules_applied"`
	ErrorMessage  string `json:"error_message,omitempty"`
}

func (h *Handler) agentReportFilterState(w http.ResponseWriter, r *http.Request) {
	if _, ok := devicemgmt.AuthenticateAgent(w, r, h.devices); !ok {
		return
	}
	deviceID := chi.URLParam(r, "id")
	var req agentFilterReportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Status == "" {
		req.Status = "synced"
	}

	state := &DeviceFilterState{
		DeviceID:      deviceID,
		PolicyVersion: req.PolicyVersion,
		Status:        req.Status,
		RulesApplied:  req.RulesApplied,
		ErrorMessage:  req.ErrorMessage,
	}

	if err := h.repo.RecordDeviceFilterState(r.Context(), state); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

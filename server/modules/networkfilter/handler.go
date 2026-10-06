package networkfilter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// Compile-time proof the handler satisfies the transport hook. Without it a
// signature change here would only surface as a silent no-op at the reconnect
// site, because that call is guarded by a nil check.
var _ transport.FilterSyncer = (*Handler)(nil)

// device_filter_states.status is a closed set, so the values live here rather than
// being spelled out at each use site.
const (
	// statusSynced means the agent applied the policy at both enforcement layers.
	statusSynced = "synced"
	// statusDegraded means the rules reached the device but enforcement is weaker
	// than requested: the firewall layer was refused, or some domains did not
	// resolve. Distinct from 'failed' because the hosts layer did apply, and
	// distinct from 'synced' because a device blocking only via the hosts file
	// still lets every subdomain of a blocked domain through.
	statusDegraded = "degraded"
	// statusPending is what an operator-side dispatch reports before the device has
	// answered.
	statusPending = "pending"
	// statusFailed means the agent could not apply the policy at all.
	statusFailed = "failed"
)

// validStatus is the closed set above, as a lookup. The report endpoint writes a
// status string that came from the device, so without this the column could hold
// any text at all while its own definition calls it a closed set -- and the
// console renders whatever is in there.
var validStatus = map[string]bool{
	statusSynced:   true,
	statusDegraded: true,
	statusPending:  true,
	statusFailed:   true,
}

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

// updatePolicyReq is deliberately not createPolicyReq. A partial update has to
// tell "the caller did not mention this" apart from "the caller sent it empty",
// and a plain string cannot: JSON leaves both as "". Assigning the decoded zero
// straight onto the policy wiped the scope on every partial write, so the
// console's enable/disable toggle -- which sends only {is_enabled} -- blanked
// target_id and then failed validateTarget with a 400. Disabling a scoped
// policy was impossible through the UI. Pointers restore the distinction.
//
// Name and TargetType additionally reject an explicit empty string: neither an
// unnamed policy nor an unrecognised scope is a meaningful value, so an empty
// one is a mistake to ignore rather than a field to clear. TargetID does accept
// empty, because clearing the scope is exactly how a policy returns to "all".
type updatePolicyReq struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	TargetType  *string `json:"target_type"`
	TargetID    *string `json:"target_id"`
	IsEnabled   *bool   `json:"is_enabled"`
	Priority    *int    `json:"priority"`
}

// validTargetTypes is closed on purpose. An unrecognised scope used to be stored
// verbatim and then matched nothing, so the policy looked configured and applied
// to nobody.
var validTargetTypes = map[string]bool{"all": true, "group": true, "device": true}

// validateTarget refuses a policy whose scope names a device or group that does
// not exist. Without this, a mistyped ID is accepted, saved, listed in the policy
// table, and silently blocks nothing -- the operator's only clue is that the
// device's rule count never moves.
func (h *Handler) validateTarget(ctx context.Context, targetType, targetID string) error {
	if !validTargetTypes[targetType] {
		return fmt.Errorf("target_type must be one of all, group, device (got %q)", targetType)
	}
	// "all" is the whole fleet, so there is nothing to point at and an empty ID is
	// its correct value rather than a missing one.
	if targetType == "all" {
		return nil
	}
	if targetID == "" {
		return fmt.Errorf("target_id is required when target_type is %q", targetType)
	}
	if targetType == "device" {
		if _, err := h.devices.GetByID(ctx, targetID); err != nil {
			return fmt.Errorf("device %q not found", targetID)
		}
		return nil
	}
	var exists int
	err := h.repo.db.GetContext(ctx, &exists, `SELECT COUNT(*) FROM device_groups WHERE id = ?`, targetID)
	if err != nil {
		return fmt.Errorf("look up group: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("device group %q not found", targetID)
	}
	return nil
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
	if err := h.validateTarget(r.Context(), req.TargetType, req.TargetID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
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

	var req updatePolicyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != nil {
		if *req.Name == "" {
			writeErr(w, http.StatusBadRequest, "name must not be empty")
			return
		}
		policy.Name = *req.Name
	}
	if req.Description != nil {
		policy.Description = *req.Description
	}
	if req.TargetType != nil {
		if *req.TargetType == "" {
			writeErr(w, http.StatusBadRequest, "target_type must not be empty")
			return
		}
		policy.TargetType = *req.TargetType
	}
	if req.TargetID != nil {
		policy.TargetID = *req.TargetID
	}
	if err := h.validateTarget(r.Context(), policy.TargetType, policy.TargetID); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.IsEnabled != nil {
		policy.IsEnabled = *req.IsEnabled
	}
	if req.Priority != nil {
		if *req.Priority <= 0 {
			writeErr(w, http.StatusBadRequest, "priority must be greater than zero")
			return
		}
		policy.Priority = *req.Priority
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
	// The two rejected combinations are refused with an explanation rather than
	// stored. Both were accepted and then discarded later: CompileEffectiveRules
	// filters on action = 'block', so an 'allow' rule was dropped at enforcement
	// time, and 'ip_port' reached the hosts file where the engine strips everything
	// after the first "/" -- "198.51.100.0/24:443" was written as a bare hostname.
	// Neither failure was visible from the console.
	if req.Action != "" && req.Action != "block" {
		writeErr(w, http.StatusBadRequest,
			"action must be \"block\": enforcement unions every block rule and has no allow/exempt path, so an allow rule would never take effect")
		return
	}
	if req.RuleType != "" && req.RuleType != "domain" {
		writeErr(w, http.StatusBadRequest,
			"rule_type must be \"domain\": filtering is name-based, and a host rule cannot express a CIDR or a port")
		return
	}

	rule := &FilterRule{
		PolicyID: policyID,
		RuleType: "domain",
		Pattern:  req.Pattern,
		Action:   "block",
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

// filterApplyEnvelope builds the wire message an agent applies. Both the manual
// sync and the reconnect re-sync send exactly this, so a device cannot end up
// holding one shape of policy from one path and another shape from the other.
func filterApplyEnvelope(version string, patterns []string) ([]byte, error) {
	env := transport.Envelope{
		Type:    transport.TypeCommand,
		ID:      version,
		Command: "filter.apply",
		Payload: map[string]any{
			"policy_version":  version,
			"blocked_domains": patterns,
		},
	}
	return json.Marshal(env)
}

// SyncOnReconnect re-pushes the compiled policy to a device that just came back
// online holding a version the server no longer considers current.
//
// This is the counterpart to a manual sync that was clicked while the device was
// asleep: that path wrote status='pending' and returned, and nothing ever read the
// row, so the device stayed unfiltered until an operator clicked Sync again. The
// version check is what keeps this from re-pushing the same rule set to the whole
// fleet on every reconnect -- a device that reports 'synced' at the current version
// sends nothing.
//
// A device that reports anything other than 'synced' IS re-sent, including one
// already at the current version. 'failed' and 'degraded' both mean the rules are
// there and not being enforced, and re-running apply is the only thing that can fix
// that: a service restart that lands the agent under SYSTEM with the same policy
// now succeeds where the previous attempt was refused for lack of elevation.
func (h *Handler) SyncOnReconnect(ctx context.Context, deviceID string) error {
	if h.hub == nil || !h.hub.Online(deviceID) {
		return nil
	}
	patterns, version, err := h.repo.CompileEffectiveRules(ctx, deviceID)
	if err != nil {
		return fmt.Errorf("compile rules: %w", err)
	}
	state, err := h.repo.GetDeviceFilterState(ctx, deviceID)
	switch {
	case err == nil && state.PolicyVersion == version && state.Status == statusSynced:
		return nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("read filter state: %w", err)
	}

	payload, err := filterApplyEnvelope(version, patterns)
	if err != nil {
		return fmt.Errorf("encode filter policy: %w", err)
	}
	if !h.hub.SendTo(deviceID, payload) {
		return fmt.Errorf("send filter policy for device %s", deviceID)
	}
	log.Info().Str("device", deviceID).Str("version", version).
		Int("rules", len(patterns)).Msg("re-synced filter policy on reconnect")
	return nil
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

	envBytes, err := filterApplyEnvelope(version, patterns)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "encode filter policy: "+err.Error())
		return
	}

	dispatched := false
	if h.hub != nil && h.hub.Online(deviceID) {
		dispatched = h.hub.SendTo(deviceID, envBytes)
	}

	// rules_applied stays at whatever the agent last reported. This dispatch has
	// not been confirmed, so writing this policy's rule count here would claim the
	// device is enforcing N rules the moment the bytes leave the server.
	priorRules := 0
	if prior, err := h.repo.GetDeviceFilterState(r.Context(), deviceID); err == nil {
		priorRules = prior.RulesApplied
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// The status in the response tells the console which of two honest messages to
	// show, so it answers a question about the wire, not about enforcement: the
	// device has not applied anything yet either way. 'pending' is that state, and
	// it is the value the reconnect hook and the state row both use, so the row
	// written here cannot disagree with the response.
	//
	// The row is written before the response, not left to the agent's report. The
	// agent only reports after it has applied the policy, so with no write here the
	// DB kept whatever the last report said -- a device that was pushed a new policy
	// and had not yet answered still read as its previous status, and a reloaded page
	// showed a state that no longer described the device.
	state := &DeviceFilterState{
		DeviceID:      deviceID,
		PolicyVersion: version,
		Status:        statusPending,
		RulesApplied:  priorRules,
	}
	if err := h.repo.RecordDeviceFilterState(r.Context(), state); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", actorID, "filter.sync_dispatched", deviceID, map[string]string{
		// strconv, not string(rune(...)): the rune conversion turns a count of
		// 12 into a control character rather than the text "12".
		"version": version, "rule_count": strconv.Itoa(len(patterns)),
	})

	writeJSON(w, http.StatusOK, map[string]any{
		// Reported separately from the stored status. Collapsing them would force one
		// value to mean both "the bytes are on the wire" and "the row says pending",
		// which is how 'dispatched' came to be a filter status at all -- it is the
		// command-lifecycle vocabulary of every other module (agent update, software
		// deployment, maintenance, patch, remote exec), where it describes a task on
		// the wire. It is not a statement about enforcement, so it is not a value of
		// device_filter_states.status.
		"dispatched":      dispatched,
		"status":          state.Status,
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
			"status":         statusPending,
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
	authedID, ok := devicemgmt.AuthenticateAgent(w, r, h.devices)
	if !ok {
		return
	}
	// The authenticated identity wins over the one in the path. AuthenticateAgent
	// only proves the caller holds a valid device secret; the {id} segment is
	// still attacker-controlled, and this is the fourth handler in the codebase
	// where that distinction was dropped -- the other three are fixed in
	// software-deployment, taskscheduler and patch-management. Using the path
	// value here let any enrolled agent overwrite another device's filter state,
	// which is the row the console reads to say whether an endpoint is enforcing
	// web filtering.
	deviceID := authedID
	if pathID := chi.URLParam(r, "id"); pathID != "" && pathID != authedID {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	var req agentFilterReportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Status == "" {
		req.Status = statusSynced
	}
	// The agent produces exactly synced, degraded and failed. Anything else means
	// the payload is not from an agent speaking this protocol, and storing it would
	// put a value in a column that documents itself as a closed set -- which the
	// console then renders as an enforcement state the code cannot explain.
	if !validStatus[req.Status] {
		writeErr(w, http.StatusBadRequest, "status must be one of synced, degraded, pending, failed (got "+strconv.Quote(req.Status)+")")
		return
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

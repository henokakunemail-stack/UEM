package taskscheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

type AuditLogger interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

type Handler struct {
	repo           *Repository
	scheduler      *Scheduler
	audit          AuditLogger
	authMiddleware func(http.Handler) http.Handler
	// devices authenticates agent-facing endpoints by the per-device secret.
	// Agent endpoints have no user JWT, so without this any client that can
	// reach the port could write results for arbitrary scheduled tasks.
	devices devicemgmt.SecretLookup
}

func NewHandler(repo *Repository, scheduler *Scheduler, audit AuditLogger, authMiddleware func(http.Handler) http.Handler, devices devicemgmt.SecretLookup) *Handler {
	return &Handler{
		repo:           repo,
		scheduler:      scheduler,
		audit:          audit,
		authMiddleware: authMiddleware,
		devices:        devices,
	}
}

func (h *Handler) Register(r chi.Router) {
	// Script Repository
	r.With(h.authMiddleware).Get("/api/scripts", h.listScripts)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).Post("/api/scripts", h.createScript)
	r.With(h.authMiddleware).Get("/api/scripts/{id}", h.getScript)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).Put("/api/scripts/{id}", h.updateScript)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).Delete("/api/scripts/{id}", h.deleteScript)

	// Task Schedules
	r.With(h.authMiddleware).Get("/api/schedules", h.listSchedules)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).Post("/api/schedules", h.createSchedule)
	r.With(h.authMiddleware).Get("/api/schedules/{id}", h.getSchedule)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).Put("/api/schedules/{id}", h.updateSchedule)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).Delete("/api/schedules/{id}", h.deleteSchedule)

	// Schedule Execution & History
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).Post("/api/schedules/{id}/trigger", h.triggerSchedule)
	// Fleet-wide run history. Chi keeps this distinct from the {id} route above
	// because it has one segment fewer, and ListRuns already treats an empty
	// scheduleID as "every run" — so no separate handler is needed.
	r.With(h.authMiddleware).Get("/api/schedules/runs", h.listScheduleRuns)
	r.With(h.authMiddleware).Get("/api/schedules/{id}/runs", h.listScheduleRuns)
	r.With(h.authMiddleware).Get("/api/schedules/runs/{runId}", h.getRun)
	r.With(h.authMiddleware).Get("/api/schedules/runs/{runId}/devices", h.listDeviceRuns)

	// Agent Task Result Reporting
	r.Post("/api/agent/schedules/tasks/{id}/result", h.reportTaskResult)
}

// --- Scripts Handlers ---

func (h *Handler) listScripts(w http.ResponseWriter, r *http.Request) {
	scripts, err := h.repo.ListScripts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if scripts == nil {
		scripts = []ScriptTemplate{}
	}
	writeJSON(w, http.StatusOK, scripts)
}

type createScriptReq struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	ScriptType     string `json:"script_type"`
	ScriptContent  string `json:"script_content"`
	DefaultArgs    string `json:"default_args"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (h *Handler) createScript(w http.ResponseWriter, r *http.Request) {
	var req createScriptReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Name == "" || req.ScriptType == "" || req.ScriptContent == "" {
		writeErr(w, http.StatusBadRequest, "name, script_type, and script_content are required")
		return
	}

	userID := auth.UserIDFromContext(r.Context())
	s := &ScriptTemplate{
		Name:           req.Name,
		Description:    req.Description,
		ScriptType:     req.ScriptType,
		ScriptContent:  req.ScriptContent,
		DefaultArgs:    req.DefaultArgs,
		TimeoutSeconds: req.TimeoutSeconds,
		CreatedBy:      userID,
	}
	if err := h.repo.CreateScript(r.Context(), s); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.audit.Log(r.Context(), "user", userID, "script.create", s.ID, map[string]string{
		"name":        s.Name,
		"script_type": s.ScriptType,
		"sha256":      s.SHA256Hash,
	})

	writeJSON(w, http.StatusCreated, s)
}

func (h *Handler) getScript(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s, err := h.repo.GetScriptByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "script not found")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) updateScript(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req createScriptReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}

	s, err := h.repo.GetScriptByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "script not found")
		return
	}

	s.Name = req.Name
	s.Description = req.Description
	s.ScriptType = req.ScriptType
	s.ScriptContent = req.ScriptContent
	s.DefaultArgs = req.DefaultArgs
	s.TimeoutSeconds = req.TimeoutSeconds

	if err := h.repo.UpdateScript(r.Context(), s); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	userID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", userID, "script.update", s.ID, map[string]string{
		"name": s.Name,
	})

	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) deleteScript(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.DeleteScript(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}

	userID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", userID, "script.delete", id, nil)

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Schedules Handlers ---

func (h *Handler) listSchedules(w http.ResponseWriter, r *http.Request) {
	schedules, err := h.repo.ListSchedules(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if schedules == nil {
		schedules = []TaskSchedule{}
	}
	writeJSON(w, http.StatusOK, schedules)
}

type createScheduleReq struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ScriptID     string `json:"script_id"`
	TargetType   string `json:"target_type"`
	TargetID     string `json:"target_id"`
	ScheduleType string `json:"schedule_type"`
	ScheduleExpr string `json:"schedule_expr"`
	IsEnabled    bool   `json:"is_enabled"`
}

// isRunnableScheduleType reports whether the scheduler actually fires a schedule
// of this type.
//
// pollDueSchedules (scheduler.go:156) has exactly one branch, `interval`, so
// every other value stored in the column is a schedule that is listed in the
// console as enabled and will never run once. That is a worse failure than a
// missing feature: an operator watching a schedule that looks armed, on a script
// that silently never executes.
//
// cron and once are rejected rather than implemented here. Until the scheduler
// has a branch for them, accepting them is a promise the server cannot keep, and
// the 400 says so in the operator's own terms rather than leaving a schedule
// that looks armed. Widening this list is the change that adds a trigger:
// implement it in pollDueSchedules, then add the value here and to the console
// dropdown in the same commit.
//
// Set in the console's schedule form as well. Two lists, one of which is the
// whitelist, is the alternative to editing the dropdown whenever a trigger ships.
var runnableScheduleTypes = map[string]bool{"interval": true}

func isRunnableScheduleType(v string) bool { return runnableScheduleTypes[v] }

func (h *Handler) createSchedule(w http.ResponseWriter, r *http.Request) {
	var req createScheduleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Name == "" || req.ScriptID == "" || req.TargetType == "" || req.ScheduleType == "" {
		writeErr(w, http.StatusBadRequest, "name, script_id, target_type, and schedule_type are required")
		return
	}
	if !isRunnableScheduleType(req.ScheduleType) {
		writeErr(w, http.StatusBadRequest, "schedule_type must be interval: "+strconv.Quote(req.ScheduleType)+" is a trigger the scheduler does not run")
		return
	}

	// Verify script exists
	if _, err := h.repo.GetScriptByID(r.Context(), req.ScriptID); err != nil {
		writeErr(w, http.StatusBadRequest, "referenced script does not exist")
		return
	}

	userID := auth.UserIDFromContext(r.Context())
	s := &TaskSchedule{
		Name:         req.Name,
		Description:  req.Description,
		ScriptID:     req.ScriptID,
		TargetType:   req.TargetType,
		TargetID:     req.TargetID,
		ScheduleType: req.ScheduleType,
		ScheduleExpr: req.ScheduleExpr,
		IsEnabled:    req.IsEnabled,
		CreatedBy:    userID,
	}
	if err := h.repo.CreateSchedule(r.Context(), s); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.audit.Log(r.Context(), "user", userID, "schedule.create", s.ID, map[string]string{
		"name":          s.Name,
		"target_type":   s.TargetType,
		"schedule_type": s.ScheduleType,
	})

	writeJSON(w, http.StatusCreated, s)
}

func (h *Handler) getSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s, err := h.repo.GetScheduleByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "schedule not found")
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) updateSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req createScheduleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ScheduleType != "" && !isRunnableScheduleType(req.ScheduleType) {
		writeErr(w, http.StatusBadRequest, "schedule_type must be interval: "+strconv.Quote(req.ScheduleType)+" is a trigger the scheduler does not run")
		return
	}

	s, err := h.repo.GetScheduleByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "schedule not found")
		return
	}

	s.Name = req.Name
	s.Description = req.Description
	s.ScriptID = req.ScriptID
	s.TargetType = req.TargetType
	s.TargetID = req.TargetID
	s.ScheduleType = req.ScheduleType
	s.ScheduleExpr = req.ScheduleExpr
	s.IsEnabled = req.IsEnabled

	if err := h.repo.UpdateSchedule(r.Context(), s); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	userID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", userID, "schedule.update", s.ID, map[string]string{
		"name": s.Name,
	})

	writeJSON(w, http.StatusOK, s)
}

func (h *Handler) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.repo.DeleteSchedule(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}

	userID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", userID, "schedule.delete", id, nil)

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) triggerSchedule(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := auth.UserIDFromContext(r.Context())

	run, err := h.scheduler.TriggerSchedule(r.Context(), id, userID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.audit.Log(r.Context(), "user", userID, "schedule.trigger", id, map[string]string{
		"run_id": run.ID,
	})

	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) listScheduleRuns(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}

	runs, err := h.repo.ListRuns(r.Context(), id, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if runs == nil {
		runs = []ScheduledTaskRun{}
	}
	writeJSON(w, http.StatusOK, runs)
}

func (h *Handler) getRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")
	run, err := h.repo.GetRunByID(r.Context(), runID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) listDeviceRuns(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runId")
	deviceRuns, err := h.repo.ListDeviceRuns(r.Context(), runID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if deviceRuns == nil {
		deviceRuns = []ScheduledTaskDeviceRun{}
	}
	writeJSON(w, http.StatusOK, deviceRuns)
}

type taskResultReq struct {
	Status       string `json:"status"`
	ExitCode     int    `json:"exit_code"`
	OutputLog    string `json:"output_log"`
	Output       string `json:"output"`
	ErrorMessage string `json:"error_message"`
}

// normalizeTaskStatus maps the agent's vocabulary onto this module's.
//
// The executor in agent/shared/remoteexec is shared by two features, so it
// reports in its own words: "completed", "failed", "timeout". This table's
// terminal set is ('success', 'failed'), so a "completed" report landed as a
// nonterminal status. The device row then read 'completed', which is not in
// that set either, and SyncRunStatus counted it as still running -- so the run
// stayed 'running' with a NULL completed_at forever, on exactly the path that
// had just worked. Nothing in the console renders "completed" on this table,
// so the operator saw a row in a state no code path could ever close.
func normalizeTaskStatus(status string, exitCode int) string {
	switch status {
	case "completed":
		return "success"
	case "failed", "timeout":
		return "failed"
	case "success", "":
		if exitCode == 0 {
			return "success"
		}
		return "failed"
	default:
		return status
	}
}

func (h *Handler) reportTaskResult(w http.ResponseWriter, r *http.Request) {
	// AuthenticateAgent proves who is asking; it says nothing about whose run
	// this is. The {id} in the path is attacker-controlled, and UpdateDeviceRunResult
	// matched on it alone, so any enrolled agent could write a result -- status,
	// exit code and output log -- for any other device's scheduled task, and the
	// parent run would be rolled up from that. The comparison is the fix; the
	// comment on the devices field describes why the endpoint is authenticated
	// at all.
	authedID, ok := devicemgmt.AuthenticateAgent(w, r, h.devices)
	if !ok {
		return
	}
	taskID := chi.URLParam(r, "id")
	owner, err := h.repo.DeviceRunOwner(r.Context(), taskID)
	if err != nil || owner != authedID {
		// 404, not 403: the caller is a real enrolled agent, so it has no
		// business learning which run ids exist in the fleet.
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	var req taskResultReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}

	if req.OutputLog == "" {
		req.OutputLog = req.Output
	}
	req.Status = normalizeTaskStatus(req.Status, req.ExitCode)

	if err := h.repo.UpdateDeviceRunResult(r.Context(), taskID, req.Status, req.ExitCode, req.OutputLog, req.ErrorMessage); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Recompute the parent run. Without this the row CreateRun wrote as
	// 'running' is never touched again on any path where the target had at
	// least one device, so a run whose every endpoint reported stays 'running'
	// with a NULL completed_at indefinitely. The device result is already
	// recorded, so a failure here is a rollup problem and not a reason to fail
	// the agent's report -- the agent cannot retry it and would only learn it
	// as a spurious error.
	if err := h.repo.SyncRunStatus(r.Context(), taskID); err != nil {
		log.Warn().Err(err).Str("task_id", taskID).Msg("sync parent scheduled task run")
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

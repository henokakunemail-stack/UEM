package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// Hub is the slice of the socket registry the dispatcher needs. Keeping it an
// interface here is what lets the handler be tested without a live websocket.
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
	audit          AuditLogger
	authMiddleware func(http.Handler) http.Handler
	// devices authenticates the agent-facing report endpoint by the per-device
	// secret. That endpoint has no user JWT, so without it any client reaching
	// the port could report a privileged sweep as complete on another device's
	// task.
	devices devicemgmt.SecretLookup
}

func NewHandler(repo *Repository, hub Hub, audit AuditLogger,
	authMiddleware func(http.Handler) http.Handler, devices devicemgmt.SecretLookup) *Handler {
	return &Handler{repo: repo, hub: hub, audit: audit,
		authMiddleware: authMiddleware, devices: devices}
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(cr chi.Router) {
		cr.Use(h.authMiddleware)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/maintenance/tasks", h.handleGetCatalog)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/maintenance/jobs", h.handleListJobs)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/maintenance/jobs/{id}/progress", h.handleGetProgress)
		cr.With(rbac.RequireRole(rbac.RoleViewer)).Get("/api/maintenance/jobs/{id}/tasks", h.handleListJobTasks)
		cr.With(rbac.RequireRole(rbac.RoleTechnician)).Post("/api/maintenance/jobs", h.handleCreateJob)
	})
	// Agent-facing: authenticated by the per-device secret, not a user JWT, so
	// it lives outside the JWT group and never sees rbac.
	r.Post("/api/agent/maintenance/tasks/{task_id}/result", h.handleAgentResult)
}

func (h *Handler) handleGetCatalog(w http.ResponseWriter, r *http.Request) {
	// make(..., 0, n) rather than a nil slice: the console maps this straight
	// through, and null.map is a React crash.
	items := make([]TaskInfo, 0, len(TaskOrder))
	for _, id := range TaskOrder {
		if info, ok := TaskCatalog[id]; ok {
			items = append(items, info)
		}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) handleListJobs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	// The repository re-clamps both, so a hand-typed ?limit=9999 cannot ask it
	// for an unbounded page.
	jobs, _, err := h.repo.ListJobs(r.Context(), JobFilter{
		Status:     r.URL.Query().Get("status"),
		TaskType:   r.URL.Query().Get("task_type"),
		TargetType: r.URL.Query().Get("target_type"),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Bare array, not an envelope: the console feeds it straight to setJobs.
	writeJSON(w, http.StatusOK, jobs)
}

func (h *Handler) handleGetProgress(w http.ResponseWriter, r *http.Request) {
	p, err := h.repo.GetJobProgress(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "job not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The repository's JobProgress verbatim — bytes_freed and reboot_required
	// only exist on the struct.
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) handleListJobTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.repo.GetTasksForJob(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (h *Handler) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}

	// Validate task_type against the server's own catalog before anything is
	// persisted. This string is copied onto every task row and shipped to the
	// agent in a command envelope; an unknown value must never reach a dispatch
	// loop or a filesystem path on the agent.
	info, ok := TaskCatalog[req.TaskType]
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("unknown task_type %q", req.TaskType))
		return
	}

	switch req.TargetType {
	case TargetDevice, TargetGroup:
		if strings.TrimSpace(req.TargetID) == "" {
			writeErr(w, http.StatusBadRequest,
				"target_id is required for target_type "+string(req.TargetType))
			return
		}
	case TargetAll:
		req.TargetID = "" // meaningless for a fleet-wide run
	default:
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("unknown target_type %q (want device, group or all)", req.TargetType))
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = info.Label
	}

	targets, err := h.repo.ResolveTargets(r.Context(), req.TargetType, req.TargetID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reject before CreateJob so a typo or an emptied group never leaves a job
	// row behind. CreateTasksForJob also closes a zero-target job; this is the
	// early exit that keeps the history list clean.
	if len(targets) == 0 {
		writeErr(w, http.StatusBadRequest, "target resolved to 0 devices")
		return
	}

	actorID := auth.UserIDFromContext(r.Context())
	job := &Job{
		Name:       name,
		TaskType:   req.TaskType,
		TargetType: string(req.TargetType),
		TargetID:   req.TargetID,
		CreatedBy:  actorID,
	}
	if err := h.repo.CreateJob(r.Context(), job); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.repo.CreateTasksForJob(r.Context(), job.ID, req.TaskType, targets); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Every task row exists before the first socket write, so an agent that
	// replies before this loop finishes still finds a row to update.
	tasks, err := h.repo.GetTasksForJob(r.Context(), job.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	dispatched, skipped := 0, 0
	for _, task := range tasks {
		if !h.hub.Online(task.DeviceID) {
			if err := h.repo.MarkTaskSkipped(r.Context(), task.ID); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			skipped++
			continue
		}
		env := transport.Envelope{
			Type:    transport.TypeCommand,
			ID:      task.ID,
			Command: "maintenance.run",
			Payload: map[string]any{
				"task_id":   task.ID,
				"job_id":    job.ID,
				"task_type": job.TaskType,
			},
		}
		b, err := json.Marshal(env)
		if err != nil {
			continue
		}
		// A false return is a busy socket. That task must not be left pending
		// with no path to a terminal state, so it is skipped like any other
		// unreachable device. One busy box must not abort a fleet sweep.
		if h.hub.SendTo(task.DeviceID, b) {
			if err := h.repo.MarkTaskDispatched(r.Context(), task.ID); err != nil && !errors.Is(err, ErrNotFound) {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			dispatched++
		} else {
			if err := h.repo.MarkTaskSkipped(r.Context(), task.ID); err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			skipped++
		}
	}

	_ = h.audit.Log(r.Context(), "user", actorID, "maintenance.run", job.ID, map[string]string{
		"task_type":       job.TaskType,
		"target_type":     job.TargetType,
		"target_id":       job.TargetID,
		"total_targets":   strconv.Itoa(len(targets)),
		"dispatched_live": strconv.Itoa(dispatched),
		"skipped":         strconv.Itoa(skipped),
	})

	// Re-read: CreateJob inserted zeroed counters and CreateTasksForJob wrote
	// total_tasks in SQL without touching this struct. The console opens the
	// detail modal straight off res.job, so the counters have to be real.
	final, err := h.repo.GetJob(r.Context(), job.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"job":             final,
		"total_targets":   len(targets),
		"dispatched_live": dispatched,
		"skipped":         skipped,
	})
}

// handleAgentResult is the trust boundary for a privileged claim: whoever holds
// the device secret is asserting a sweep ran on that machine.
func (h *Handler) handleAgentResult(w http.ResponseWriter, r *http.Request) {
	// Authenticate by device secret first: this endpoint has no user JWT, and
	// its body is the assertion. On failure AuthenticateAgent has already
	// written the response.
	deviceID, ok := devicemgmt.AuthenticateAgent(w, r, h.devices)
	if !ok {
		return
	}

	taskID := chi.URLParam(r, "task_id")
	if taskID == "" {
		writeErr(w, http.StatusBadRequest, "missing task id")
		return
	}

	var rep StepReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json payload")
		return
	}
	// The URL is authoritative for which task; a body claiming a different one
	// is a mismatch, not a second task.
	if rep.TaskID != "" && rep.TaskID != taskID {
		writeErr(w, http.StatusBadRequest, "task_id does not match the request path")
		return
	}
	rep.TaskID = taskID

	task, err := h.repo.GetTask(r.Context(), taskID)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The device_id guard. The caller is authenticated, but the task_id in the
	// path is not, so an agent must not be able to report on another device's
	// task. This is a primary-key SELECT, not a scan.
	if task.DeviceID != deviceID {
		writeErr(w, http.StatusForbidden, "task belongs to another device")
		return
	}

	// RecordStep is the single validation point for a report: it checks the
	// status vocabulary, rejects the server-only 'skipped', verifies the step
	// belongs to the task type, and absorbs a replay. Duplicating a narrower
	// allowlist here would reject a report the repository is built to store.
	if err := h.repo.RecordStep(r.Context(), rep); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "task not found")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

// Package-local copies of the JSON helpers every other module in this server
// keeps; main.go's are unexported and cannot be shared across packages.
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

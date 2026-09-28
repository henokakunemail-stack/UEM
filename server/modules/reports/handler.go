package reports

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type Handler struct {
	repo           *Repository
	authMiddleware func(http.Handler) http.Handler
}

func NewHandler(repo *Repository, authMiddleware func(http.Handler) http.Handler) *Handler {
	return &Handler{
		repo:           repo,
		authMiddleware: authMiddleware,
	}
}

func (h *Handler) Register(r chi.Router) {
	// Technical reports accessible to Viewer+
	r.With(h.authMiddleware).Get("/api/reports/inventory", h.exportInventory)
	r.With(h.authMiddleware).Get("/api/reports/patches", h.exportPatches)
	r.With(h.authMiddleware).Get("/api/reports/deployments", h.exportDeployments)

	// Forensic audit report accessible to Admin only
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleAdmin)).
		Get("/api/reports/audit", h.exportAudit)
}

func (h *Handler) exportInventory(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	site := r.URL.Query().Get("site")
	osName := r.URL.Query().Get("os")

	rows, err := h.repo.GetDeviceInventoryReport(r.Context(), site, osName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=device-inventory-%s.csv", time.Now().Format("20060102-150405")))
		writer := csv.NewWriter(w)
		defer writer.Flush()

		_ = writer.Write([]string{"ID", "Hostname", "OS", "OS Version", "Agent Version", "Site", "Status", "RAM (Bytes)", "Disk Free (%)", "CPU", "Last Seen", "Enrolled At"})
		for _, row := range rows {
			ram := ""
			if row.RAMBytes != nil {
				ram = strconv.FormatInt(*row.RAMBytes, 10)
			}
			disk := ""
			if row.DiskFreePct != nil {
				disk = fmt.Sprintf("%.2f", *row.DiskFreePct)
			}
			cpu := ""
			if row.CPUModel != nil {
				cpu = *row.CPUModel
			}
			// nil means the device has never checked in; the zero time would
			// render as 0001-01-01 and read as a real, ancient timestamp.
			lastSeen := ""
			if row.LastSeenAt != nil {
				lastSeen = row.LastSeenAt.Format(time.RFC3339)
			}
			_ = writer.Write([]string{
				row.ID, row.Hostname, row.OSName, row.OSVersion, row.AgentVersion,
				row.Site, row.Status, ram, disk, cpu,
				lastSeen, row.EnrolledAt.Format(time.RFC3339),
			})
		}
		return
	}

	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) exportPatches(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	severity := r.URL.Query().Get("severity")
	state := r.URL.Query().Get("state")

	rows, err := h.repo.GetPatchComplianceReport(r.Context(), severity, state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=patch-compliance-%s.csv", time.Now().Format("20060102-150405")))
		writer := csv.NewWriter(w)
		defer writer.Flush()

		_ = writer.Write([]string{"Device ID", "Hostname", "OS", "Site", "Patch ID", "Title", "Severity", "Category", "State", "Reboot Required", "Discovered At"})
		for _, row := range rows {
			_ = writer.Write([]string{
				row.DeviceID, row.Hostname, row.OSName, row.Site, row.PatchID,
				row.Title, row.Severity, row.Category, row.InstalledState,
				strconv.FormatBool(row.RebootRequired), row.DiscoveredAt.Format(time.RFC3339),
			})
		}
		return
	}

	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) exportDeployments(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")

	rows, err := h.repo.GetDeploymentHistoryReport(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=deployment-history-%s.csv", time.Now().Format("20060102-150405")))
		writer := csv.NewWriter(w)
		defer writer.Flush()

		_ = writer.Write([]string{"Deployment ID", "Name", "Package", "Target Type", "Device ID", "Hostname", "Task Status", "Exit Code", "Started At", "Completed At"})
		for _, row := range rows {
			exitCode := ""
			if row.ExitCode != nil {
				exitCode = strconv.Itoa(*row.ExitCode)
			}
			started := ""
			if row.StartedAt != nil {
				started = row.StartedAt.Format(time.RFC3339)
			}
			completed := ""
			if row.CompletedAt != nil {
				completed = row.CompletedAt.Format(time.RFC3339)
			}
			_ = writer.Write([]string{
				row.DeploymentID, row.Name, row.PackageName, row.TargetType,
				row.DeviceID, row.Hostname, row.TaskStatus, exitCode, started, completed,
			})
		}
		return
	}

	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) exportAudit(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	action := r.URL.Query().Get("action")
	limitStr := r.URL.Query().Get("limit")

	limit := 1000
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}

	rows, err := h.repo.GetAuditTrailReport(r.Context(), action, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=audit-trail-%s.csv", time.Now().Format("20060102-150405")))
		writer := csv.NewWriter(w)
		defer writer.Flush()

		_ = writer.Write([]string{"ID", "Actor Type", "Actor ID", "Action", "Target ID", "Details", "Timestamp"})
		for _, row := range rows {
			target := ""
			if row.TargetID != nil {
				target = *row.TargetID
			}
			details := ""
			if row.Details != nil {
				details = *row.Details
			}
			_ = writer.Write([]string{
				row.ID, row.ActorType, row.ActorID, row.Action, target, details, row.CreatedAt.Format(time.RFC3339),
			})
		}
		return
	}

	writeJSON(w, http.StatusOK, rows)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

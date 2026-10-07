package reports

import (
	"bytes"
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
		writeCSV(w, "device-inventory-"+time.Now().Format("20060102-150405")+".csv",
			[]string{"ID", "Hostname", "OS", "OS Version", "Agent Version", "Site", "Status", "RAM (Bytes)", "Disk Free (%)", "CPU", "Last Seen", "Enrolled At"},
			func(writer *csv.Writer) error {
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
					if err := writer.Write([]string{
						row.ID, row.Hostname, row.OSName, row.OSVersion, row.AgentVersion,
						row.Site, row.Status, ram, disk, cpu,
						lastSeen, row.EnrolledAt.Format(time.RFC3339),
					}); err != nil {
						return err
					}
				}
				return nil
			})
		return
	}

	writeJSON(w, rows)
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
		writeCSV(w, "patch-compliance-"+time.Now().Format("20060102-150405")+".csv",
			[]string{"Device ID", "Hostname", "OS", "Site", "Patch ID", "Title", "Severity", "Category", "State", "Reboot Required", "Discovered At"},
			func(writer *csv.Writer) error {
				for _, row := range rows {
					if err := writer.Write([]string{
						row.DeviceID, row.Hostname, row.OSName, row.Site, row.PatchID,
						row.Title, row.Severity, row.Category, row.InstalledState,
						strconv.FormatBool(row.RebootRequired), row.DiscoveredAt.Format(time.RFC3339),
					}); err != nil {
						return err
					}
				}
				return nil
			})
		return
	}

	writeJSON(w, rows)
}

func (h *Handler) exportDeployments(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")

	rows, err := h.repo.GetDeploymentHistoryReport(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if format == "csv" {
		writeCSV(w, "deployment-history-"+time.Now().Format("20060102-150405")+".csv",
			[]string{"Deployment ID", "Name", "Package", "Target Type", "Device ID", "Hostname", "Task Status", "Exit Code", "Started At", "Completed At"},
			func(writer *csv.Writer) error {
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
					if err := writer.Write([]string{
						row.DeploymentID, row.Name, row.PackageName, row.TargetType,
						row.DeviceID, row.Hostname, row.TaskStatus, exitCode, started, completed,
					}); err != nil {
						return err
					}
				}
				return nil
			})
		return
	}

	writeJSON(w, rows)
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
		writeCSV(w, "audit-trail-"+time.Now().Format("20060102-150405")+".csv",
			[]string{"ID", "Actor Type", "Actor ID", "Action", "Target ID", "Details", "Timestamp"},
			func(writer *csv.Writer) error {
				for _, row := range rows {
					target := ""
					if row.TargetID != nil {
						target = *row.TargetID
					}
					details := ""
					if row.Details != nil {
						details = *row.Details
					}
					if err := writer.Write([]string{
						row.ID, row.ActorType, row.ActorID, row.Action, target, details, row.CreatedAt.Format(time.RFC3339),
					}); err != nil {
						return err
					}
				}
				return nil
			})
		return
	}

	writeJSON(w, rows)
}

// writeCSV renders a CSV response. All four export handlers used to write
// straight onto w via `csv.NewWriter(w)` with a deferred Flush and every
// writer.Write error discarded.
//
// That is not just a discarded log line. A csv.Writer buffers in memory, and
// the deferred Flush is what actually writes, so the handler has already
// written its Content-Type and its 200 and returned before any write failure
// is discoverable. The client gets a truncated CSV with a success status, and
// an operator saves a half-finished compliance report believing it complete.
// The error is also unwritable at that point: nothing can change the status
// after the first byte of body goes out.
//
// So this buffers, flushes explicitly, and checks writer.Error() -- which
// surfaces the first write error -- before committing any header. A failed
// report becomes a 500 instead of a convincing half file. The per-handler row
// formatting is passed in, since the column lists differ and embedding them
// here would mean reordering the format of one report from another.
func writeCSV(w http.ResponseWriter, filename string, header []string, emitRows func(*csv.Writer) error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	if err := writer.Write(header); err != nil {
		http.Error(w, "render csv header: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := emitRows(writer); err != nil {
		http.Error(w, "render csv rows: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		// Any error the encoder deferred lands here, before any byte of the
		// response body is written. Go's csv.Writer quotes rather than rejects
		// odd field content, so in practice this fires on a buffer write that
		// fails -- the point is that the status is still ours to choose.
		http.Error(w, "encode csv: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		// The client went away mid-download. Nothing to report and nothing to
		// recover: the report is simply not going to be delivered.
		return
	}
}

// writeJSON answers 200. Every report endpoint here is a successful read; the
// failure paths return early with their own status rather than going through
// this helper, so a status parameter would only ever be 200.
func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(data)
}

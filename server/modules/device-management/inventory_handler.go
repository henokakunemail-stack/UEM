package devicemanagement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

// inventoryHandler exposes the Phase 2 API: inventory storage and retrieval,
// device lifecycle (retire/restore), and static groups.
type inventoryHandler struct {
	repo *inventoryRepository
	// dependencies injected for audit logging and command dispatch
	auditDB auditWriter
	hub     hubSender
	authMW  func(http.Handler) http.Handler
}

// auditWriter is the subset of the audit package this handler needs.
type auditWriter interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

// auditBridge adapts the audit package's Log to the handler's interface, keeping
// the module decoupled from *sqlx.DB at the type level.
type auditBridge struct {
	db *sqlx.DB
}

func (a auditBridge) Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error {
	return audit.Log(ctx, a.db, actorType, actorID, action, targetID, details)
}

// NewInventoryHandler is the wiring constructor used by cmd/server.
func NewInventoryHandler(repo *InventoryRepository, db *sqlx.DB, hub hubSender) *inventoryHandler {
	return newInventoryHandler(&repo.inventoryRepository, auditBridge{db: db}, hub)
}

// hubSender checks whether a device is connected so the collect endpoint can
// tell the caller whether the request will be served now or on next reconnect,
// and delivers the collection request itself.
type hubSender interface {
	Online(deviceID string) bool
	SendTo(deviceID string, b []byte) bool
	Disconnect(deviceID string)
}

func newInventoryHandler(repo *inventoryRepository, auditDB auditWriter, hub hubSender) *inventoryHandler {
	return &inventoryHandler{repo: repo, auditDB: auditDB, hub: hub}
}

// AcceptInventory is the transport-facing entry point for agent collection
// reports. It is the exported spelling of acceptInventory so inventoryHandler
// satisfies transport.InventoryReceiver.
func (h *inventoryHandler) AcceptInventory(ctx context.Context, deviceID string, raw []byte) error {
	return h.acceptInventory(ctx, deviceID, raw)
}

// SetCapabilities stores the capability list an agent advertised in its hello
// message, so the server never sends a command the agent would silently ignore.
func (h *inventoryHandler) SetCapabilities(ctx context.Context, deviceID, capabilitiesJSON string) error {
	return h.repo.setCapabilities(ctx, deviceID, capabilitiesJSON)
}

// Register mounts the Phase 2 routes on the given router.
func (h *inventoryHandler) Register(r chi.Router) { h.routes(r) }

// WithAuth supplies the JWT middleware used by every route. The module does not
// import the auth service directly, so tests can mount the routes with a no-op
// middleware and production wiring passes the real one.
func (h *inventoryHandler) WithAuth(mw func(http.Handler) http.Handler) *inventoryHandler {
	h.authMW = mw
	return h
}

// paginateLimit clamps the requested page size to a sane bound.
const (
	defaultLimit = 50
	maxLimit     = 200
)

// pageParams reads limit/offset from the query string, applying defaults and
// bounds. Devices number in the hundreds; an unbounded list would make the
// first dashboard load heavier than it needs to be.
func pageParams(r *http.Request) (int, int) {
	limit := defaultLimit
	offset := 0
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if s := r.URL.Query().Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

// acceptInventory stores a collection report sent by an agent. It is called from
// the transport layer, which has already authenticated the device; ctx is that
// connection's context, which is still live for the whole duration of the read
// loop that called us.
//
// The report arrives as raw JSON bytes rather than typed structs on purpose:
// the agent types carry OS build tags, and the server must build on any platform.
func (h *inventoryHandler) acceptInventory(ctx context.Context, deviceID string, raw []byte) error {
	var rep inventoryReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		return fmt.Errorf("decode inventory report: %w", err)
	}

	hwJSON, _ := json.Marshal(rep.Hardware)
	swJSON, _ := json.Marshal(rep.Software)
	osJSON, _ := json.Marshal(rep.OS)

	// Refresh the cached columns the dashboard sorts and filters on, by reading
	// the same JSON the agent sent rather than a server-side mirror struct.
	hw := decodeHW(string(hwJSON))
	var ram *int64
	var freePct *float64
	var cpuModel *string
	if hw.RAMTotalBytes > 0 {
		v := hw.RAMTotalBytes
		ram = &v
	}
	if n := len(hw.Disks); n > 0 {
		// Report the most constrained volume: that is the one an admin acts on.
		worst := hw.Disks[0]
		worstPct := freePctOf(worst.TotalBytes, worst.FreeBytes)
		for _, d := range hw.Disks[1:] {
			p := freePctOf(d.TotalBytes, d.FreeBytes)
			if p < worstPct {
				worst, worstPct = d, p
			}
		}
		freePct = &worstPct
	}
	if hw.CPU.Name != "" {
		v := hw.CPU.Name
		cpuModel = &v
	}

	// Diff against the previous snapshot so a hardware swap reaches the audit
	// trail instead of being silently overwritten.
	old, err := h.repo.getInventory(ctx, deviceID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if old.DeviceID != "" {
		d := diffInventory(old.HW, string(hwJSON), old.OSDetail, string(osJSON))
		sw := diffSoftware(old.Software, string(swJSON))
		if d.auditWorthy() {
			_ = h.auditDB.Log(ctx, "agent", deviceID, "inventory.hw_changed", deviceID,
				map[string]string{"changes": d.summary(sw)})
		}
	}

	collected := rep.CollectedAt
	return h.repo.upsertInventory(ctx, DeviceInventory{
		ID:            NewID(),
		DeviceID:      deviceID,
		HW:            string(hwJSON),
		Software:      string(swJSON),
		OSDetail:      string(osJSON),
		HWRAMBytes:    ram,
		HWDiskFreePct: freePct,
		HWCPUModel:    cpuModel,
		CollectedAt:   time.Time(collected),
	})
}

// freePctOf returns the percentage of a volume that is free, or 100 when the
// total is unknown.
func freePctOf(total, free int64) float64 {
	if total <= 0 {
		return 100
	}
	return float64(free) / float64(total) * 100
}

// --- HTTP handlers ---

// routes registers the Phase 2 routes under /api. It uses Group, not Route:
// Handler.Register already mounts /api on the same root router, and chi's Route
// calls Mount, which panics on a duplicate prefix. Group applies middleware
// inline without mounting, so two handlers can share a prefix and each own a
// disjoint set of paths.
func (h *inventoryHandler) routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.With(h.authMW, rbac.RequireRole(rbac.RoleViewer)).
			Get("/api/devices/{id}/inventory", h.getInventory)
		r.With(h.authMW, rbac.RequireRole(rbac.RoleTechnician)).
			Post("/api/devices/{id}/inventory/collect", h.collectNow)
		r.With(h.authMW, rbac.RequireRole(rbac.RoleAdmin)).
			Post("/api/devices/{id}/retire", h.retireDevice)
		r.With(h.authMW, rbac.RequireRole(rbac.RoleAdmin)).
			Post("/api/devices/{id}/restore", h.restoreDevice)

		r.With(h.authMW, rbac.RequireRole(rbac.RoleViewer)).Get("/api/groups", h.listGroups)
		r.With(h.authMW, rbac.RequireRole(rbac.RoleAdmin)).Post("/api/groups", h.createGroup)
		r.With(h.authMW, rbac.RequireRole(rbac.RoleAdmin)).Delete("/api/groups/{id}", h.deleteGroup)
		r.With(h.authMW, rbac.RequireRole(rbac.RoleViewer)).Get("/api/groups/{id}/devices", h.listGroupDevices)
		r.With(h.authMW, rbac.RequireRole(rbac.RoleAdmin)).Post("/api/groups/{id}/members", h.addMembers)
		r.With(h.authMW, rbac.RequireRole(rbac.RoleAdmin)).
			Delete("/api/groups/{id}/members/{deviceId}", h.removeMember)
	})
}

// getDevice returns the device row; used by handlers that must reject unknown or
// retired devices before issuing a request to them.

// inventoryReport is the payload an agent sends. The sections stay as raw JSON:
// the agent's typed structs carry OS build tags that must not reach the server
// build, so the server never imports them.
type inventoryReport struct {
	Hardware    json.RawMessage `json:"hardware"`
	Software    json.RawMessage `json:"software"`
	OS          json.RawMessage `json:"os"`
	CollectedAt timeOrZero      `json:"collected_at"`
}

// getInventory returns the last collected snapshot.
func (h *inventoryHandler) getInventory(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	inv, err := h.repo.getInventory(r.Context(), deviceID)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no inventory collected for this device yet")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The three sections are already JSON; decode them so the response is one
	// object rather than a stringified JSON blob inside JSON.
	resp := map[string]any{
		"device_id":    inv.DeviceID,
		"collected_at": inv.CollectedAt,
		"hw":           json.RawMessage(inv.HW),
		"software":     json.RawMessage(inv.Software),
		"os":           json.RawMessage(inv.OSDetail),
	}
	if inv.HWRAMBytes != nil {
		resp["ram_bytes"] = *inv.HWRAMBytes
	}
	if inv.HWDiskFreePct != nil {
		resp["disk_free_pct"] = *inv.HWDiskFreePct
	}
	if inv.HWCPUModel != nil {
		resp["cpu_model"] = *inv.HWCPUModel
	}
	writeJSON(w, http.StatusOK, resp)
}

// collectNow asks a connected agent to collect inventory now. Offline devices
// cannot be reached: the call is recorded as queued, meaning the periodic
// schedule will supply the data on the agent's next report.
func (h *inventoryHandler) collectNow(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if _, err := h.repo.getDevice(r.Context(), deviceID); err != nil {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}
	if !h.hub.Online(deviceID) {
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "queued", "detail": "device offline; will collect on next report"})
		return
	}
	env, _ := json.Marshal(map[string]any{
		"type": protocol.TypeInventoryCollect, "id": NewID(), "ts": nowUTC().Format(time.RFC3339),
	})
	if !h.hub.SendTo(deviceID, env) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "device connection is busy"})
		return
	}
	_ = h.auditDB.Log(r.Context(), "user", auth.UserIDFromContext(r.Context()),
		"inventory.collect", deviceID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

func (h *inventoryHandler) retireDevice(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if err := h.repo.retire(r.Context(), deviceID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "device not found or already retired")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.hub.Disconnect(deviceID)
	_ = h.auditDB.Log(r.Context(), "user", auth.UserIDFromContext(r.Context()),
		"device.retire", deviceID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "retired"})
}

func (h *inventoryHandler) restoreDevice(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if err := h.repo.restore(r.Context(), deviceID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "device not found or not retired")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.auditDB.Log(r.Context(), "user", auth.UserIDFromContext(r.Context()),
		"device.restore", deviceID, nil)
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "offline", "detail": "re-enroll the device to restore connectivity"})
}

func (h *inventoryHandler) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.repo.listGroups(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups, "count": len(groups)})
}

func (h *inventoryHandler) createGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	// Names are UNIQUE in the schema, and the group picker renders them, so two
	// groups that display identically would be indistinguishable to whoever
	// retargets a run. 409 names the clash; 500 would report a server fault for
	// what is a duplicate submission.
	if _, err := h.repo.getGroupByName(r.Context(), name); err == nil {
		writeErr(w, http.StatusConflict, "a group named "+name+" already exists")
		return
	} else if !errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	g := DeviceGroup{
		ID:          NewID(),
		Name:        name,
		Description: strings.TrimSpace(req.Description),
		CreatedAt:   nowUTC(),
		UpdatedAt:   nowUTC(),
	}
	if err := h.repo.createGroup(r.Context(), g); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.auditDB.Log(r.Context(), "user", auth.UserIDFromContext(r.Context()),
		"group.create", g.ID, map[string]string{"name": g.Name})
	writeJSON(w, http.StatusCreated, g)
}

func (h *inventoryHandler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")
	if err := h.repo.deleteGroup(r.Context(), groupID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "group not found")
			return
		}
		// 409, not 500: nothing is wrong with the server, the group is simply
		// still the target of something. The message names the blocker so the
		// operator knows what to retarget first.
		if errors.Is(err, ErrGroupInUse) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.auditDB.Log(r.Context(), "user", auth.UserIDFromContext(r.Context()),
		"group.delete", groupID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *inventoryHandler) listGroupDevices(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")
	limit, offset := pageParams(r)

	total, err := h.repo.countGroupDevices(r.Context(), groupID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	devices, err := h.repo.listGroupDevices(r.Context(), groupID, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dtos := make([]deviceDTO, 0, len(devices))
	for _, d := range devices {
		dtos = append(dtos, toDTO(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": dtos, "count": len(dtos), "total": total,
		"limit": limit, "offset": offset,
	})
}

func (h *inventoryHandler) addMembers(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")
	var req struct {
		DeviceIDs []string `json:"device_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.DeviceIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "device_ids is required")
		return
	}
	if _, err := h.repo.getGroup(r.Context(), groupID); err != nil {
		writeErr(w, http.StatusNotFound, "group not found")
		return
	}
	added, err := h.repo.addMembers(r.Context(), groupID,
		auth.UserIDFromContext(r.Context()), req.DeviceIDs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.auditDB.Log(r.Context(), "user", auth.UserIDFromContext(r.Context()),
		"group.members.add", groupID, map[string]string{"added": strconv.Itoa(added)})
	writeJSON(w, http.StatusOK, map[string]any{"added": added})
}

func (h *inventoryHandler) removeMember(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")
	deviceID := chi.URLParam(r, "deviceId")
	if err := h.repo.removeMember(r.Context(), groupID, deviceID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "device is not a member of this group")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = h.auditDB.Log(r.Context(), "user", auth.UserIDFromContext(r.Context()),
		"group.members.remove", groupID, map[string]string{"device_id": deviceID})
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

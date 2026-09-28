package remoteexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

type AuditLogger interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

type DeviceValidator interface {
	FindBySecretHash(ctx context.Context, secretHash string) (devicemgmt.Device, error)
	GetByID(ctx context.Context, id string) (devicemgmt.Device, error)
}

type Handler struct {
	repo           *Repository
	hub            *transport.Hub
	relay          *TerminalRelay
	audit          AuditLogger
	jwtSvc         *auth.JWTService
	authMiddleware func(http.Handler) http.Handler
	devices        DeviceValidator
	upgrader       websocket.Upgrader
}

func NewHandler(
	repo *Repository,
	hub *transport.Hub,
	relay *TerminalRelay,
	audit AuditLogger,
	jwtSvc *auth.JWTService,
	authMiddleware func(http.Handler) http.Handler,
	devices DeviceValidator,
	checkOrigin auth.OriginChecker,
) *Handler {
	if checkOrigin == nil {
		checkOrigin = auth.ValidateWebSocketOrigin
	}
	return &Handler{
		repo:           repo,
		hub:            hub,
		relay:          relay,
		audit:          audit,
		jwtSvc:         jwtSvc,
		authMiddleware: authMiddleware,
		devices:        devices,
		upgrader: websocket.Upgrader{
			CheckOrigin: checkOrigin,
		},
	}
}

func (h *Handler) Register(r chi.Router) {
	// 1. Remote command execution (REST)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).
		Post("/api/devices/{id}/exec", h.executeCommand)

	r.With(h.authMiddleware).
		Get("/api/devices/{id}/executions", h.listExecutions)

	r.With(h.authMiddleware).
		Get("/api/devices/{id}/executions/{execId}", h.getExecution)

	// 2. Agent reporting endpoint (authenticated via agent secret)
	r.Post("/api/agent/executions/{id}/result", h.reportExecutionResult)

	// 3. Interactive Terminal WebSocket (authenticated via query param token)
	r.Get("/api/devices/{id}/terminal/ws", h.handleTerminalWS)

	// 4. Terminal session history
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).
		Get("/api/devices/{id}/terminal/sessions", h.listTerminalSessions)
}

func (h *Handler) executeCommand(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if _, err := h.devices.GetByID(r.Context(), deviceID); err != nil {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}

	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json payload")
		return
	}

	if req.Command == "" {
		writeErr(w, http.StatusBadRequest, "command cannot be empty")
		return
	}

	if req.Shell == "" {
		req.Shell = ShellPowerShell
	}
	switch req.Shell {
	case ShellPowerShell, ShellCMD, ShellBash, ShellSH:
		// Valid shells
	default:
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("unsupported shell %q (use powershell, cmd, bash, or sh)", req.Shell))
		return
	}

	if req.TimeoutSec <= 0 {
		req.TimeoutSec = 60
	} else if req.TimeoutSec > 300 {
		req.TimeoutSec = 300
	}

	if h.hub == nil || !h.hub.Online(deviceID) {
		writeErr(w, http.StatusConflict, "device is offline")
		return
	}

	execID := NewID()
	actorID := auth.UserIDFromContext(r.Context())
	now := time.Now().UTC()

	exec := &RemoteExecution{
		ID:          execID,
		DeviceID:    deviceID,
		OperatorID:  actorID,
		ShellType:   req.Shell,
		CommandText: req.Command,
		Status:      ExecStatusRunning,
		StartedAt:   now,
	}

	if err := h.repo.CreateExecution(r.Context(), exec); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to record execution: "+err.Error())
		return
	}

	cmdPayload := map[string]any{
		"execution_id": execID,
		"shell":        req.Shell,
		"command":      req.Command,
		"timeout_sec":  req.TimeoutSec,
	}
	env := transport.Envelope{
		Type:    transport.TypeCommand,
		ID:      execID,
		Command: "exec.run",
		Payload: cmdPayload,
	}
	envBytes, err := json.Marshal(env)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to encode command: "+err.Error())
		return
	}

	if !h.hub.SendTo(deviceID, envBytes) {
		_ = h.repo.UpdateExecutionResult(r.Context(), ExecResultReport{
			ExecutionID:  execID,
			Status:       ExecStatusFailed,
			ErrorMessage: strPtr("device connection busy or closed during dispatch"),
		})
		writeErr(w, http.StatusConflict, "failed to dispatch command to device socket")
		return
	}

	_ = h.audit.Log(r.Context(), "user", actorID, "remote_exec.run", deviceID, map[string]string{
		"execution_id": execID,
		"shell":        req.Shell,
		"command":      req.Command,
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":    "dispatched",
		"execution": exec,
	})
}

func (h *Handler) listExecutions(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	rows, err := h.repo.ListExecutions(r.Context(), deviceID, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []RemoteExecution{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) getExecution(w http.ResponseWriter, r *http.Request) {
	execID := chi.URLParam(r, "execId")
	exec, err := h.repo.GetExecution(r.Context(), execID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "execution not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, exec)
}

func (h *Handler) reportExecutionResult(w http.ResponseWriter, r *http.Request) {
	execID := chi.URLParam(r, "id")
	deviceID := r.Header.Get("X-Device-Id")
	secret := r.Header.Get("X-Device-Secret")

	if deviceID == "" || secret == "" {
		writeErr(w, http.StatusUnauthorized, "missing device credentials")
		return
	}

	dev, err := h.devices.FindBySecretHash(r.Context(), devicemgmt.HashToken(secret))
	if err != nil || dev.ID != deviceID {
		writeErr(w, http.StatusUnauthorized, "invalid device credentials")
		return
	}

	var rep ExecResultReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	rep.ExecutionID = execID

	if err := h.repo.UpdateExecutionResult(r.Context(), rep); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = h.audit.Log(r.Context(), "agent", deviceID, "remote_exec.result", execID, map[string]string{
		"execution_id": execID,
		"status":       rep.Status,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

// authenticateHandshake decides who is opening a terminal socket and writes the
// refusal itself when the answer is no. It returns the claims only on success,
// so a caller cannot forget to check the bool.
//
// A ticket is the intended credential: the console fetches one over an ordinary
// authenticated request, so no JWT ever appears in a URL that gets logged,
// cached, or sent in a Referer. The ?token= path stays for clients that have not
// switched yet, and it is kept safe rather than merely tolerated -- a refresh
// token is refused there, which is the one thing that path was missing.
func (h *Handler) authenticateHandshake(w http.ResponseWriter, r *http.Request) (auth.Claims, bool) {
	if ticket := auth.TicketFromRequest(r); ticket != "" {
		userID, ok := auth.RedeemWebSocketTicket(r.Context(), ticket, auth.PurposeRemoteExec)
		if !ok {
			http.Error(w, "invalid or expired ticket", http.StatusUnauthorized)
			return auth.Claims{}, false
		}
		// A ticket names a user, not a set of privileges, so the role is read
		// from the live users row. Deriving it from anything the ticket carried
		// would give the ticket a second, separately expiring copy of an
		// authorisation decision.
		claims, ok := auth.ClaimsForUser(r.Context(), auth.CurrentRoleReader(), userID)
		if !ok {
			http.Error(w, "invalid or expired ticket", http.StatusUnauthorized)
			return auth.Claims{}, false
		}
		return claims, true
	}

	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		http.Error(w, "missing authentication", http.StatusUnauthorized)
		return auth.Claims{}, false
	}
	// ParseKind, not Parse: a refresh token presented as a Bearer credential
	// would open a remote shell for its whole TTL. The handshake cannot carry
	// the Authorization header that RequireAuth checks, so this is the only
	// place that can refuse it.
	claims, err := h.jwtSvc.ParseKind(tokenStr, auth.KindAccess)
	if err != nil {
		http.Error(w, "invalid authentication token", http.StatusUnauthorized)
		return auth.Claims{}, false
	}
	return claims, true
}

func (h *Handler) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	shell := r.URL.Query().Get("shell")
	if shell == "" {
		shell = ShellPowerShell
	}

	claims, ok := h.authenticateHandshake(w, r)
	if !ok {
		return // the refusal is already written
	}

	if claims.Role != rbac.RoleTechnician && claims.Role != rbac.RoleAdmin {
		http.Error(w, "insufficient privileges for remote terminal (technician or admin required)", http.StatusForbidden)
		return
	}

	if h.hub == nil || !h.hub.Online(deviceID) {
		http.Error(w, "device is currently offline", http.StatusConflict)
		return
	}

	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Str("device", deviceID).Msg("upgrade terminal websocket")
		return
	}

	sessionID := NewID()
	now := time.Now().UTC()

	sessionRecord := &TerminalSession{
		ID:         sessionID,
		DeviceID:   deviceID,
		OperatorID: claims.UserID,
		ShellType:  shell,
		Status:     TermStatusActive,
		CreatedAt:  now,
	}

	if err := h.repo.CreateTerminalSession(r.Context(), sessionRecord); err != nil {
		log.Error().Err(err).Msg("create terminal session record")
	}

	_ = h.audit.Log(r.Context(), "user", claims.UserID, "terminal.open", deviceID, map[string]string{
		"session_id": sessionID,
		"shell":      shell,
	})

	sess := h.relay.Register(sessionID, deviceID, claims.UserID, ws)

	// Send term.open command to target agent
	cmdPayload := map[string]any{
		"session_id": sessionID,
		"shell":      shell,
	}
	openEnv := transport.Envelope{
		Type:    transport.TypeCommand,
		ID:      sessionID,
		Command: "term.open",
		Payload: cmdPayload,
	}
	openBytes, _ := json.Marshal(openEnv)
	h.hub.SendTo(deviceID, openBytes)

	// Notify browser that session is opened
	_ = sess.WriteToBrowser("term.open", fmt.Sprintf("Terminal session %s established with %s.", sessionID, deviceID))

	defer func() {
		h.relay.Unregister(sessionID)
		_ = ws.Close()

		// Instruct agent to kill shell process
		closeEnv := transport.Envelope{
			Type:    transport.TypeCommand,
			ID:      sessionID,
			Command: "term.close",
			Payload: map[string]any{"session_id": sessionID},
		}
		closeBytes, _ := json.Marshal(closeEnv)
		if h.hub != nil {
			h.hub.SendTo(deviceID, closeBytes)
		}

		_ = h.repo.CloseTerminalSession(context.Background(), sessionID)
		_ = h.audit.Log(context.Background(), "user", claims.UserID, "terminal.close", deviceID, map[string]string{
			"session_id": sessionID,
		})
	}()

	// Browser read loop: receives input from operator and forwards to agent
	for {
		_, msgBytes, err := ws.ReadMessage()
		if err != nil {
			break
		}

		var clientMsg struct {
			Type string `json:"type"` // "term.data", "term.close"
			Data string `json:"data"`
		}
		if err := json.Unmarshal(msgBytes, &clientMsg); err != nil {
			continue
		}

		if clientMsg.Type == "term.close" {
			break
		}

		if clientMsg.Type == "term.data" && clientMsg.Data != "" {
			dataEnv := transport.Envelope{
				Type:    transport.TypeCommand,
				ID:      sessionID,
				Command: "term.data",
				Payload: map[string]any{
					"session_id": sessionID,
					"data":       clientMsg.Data,
				},
			}
			b, _ := json.Marshal(dataEnv)
			if h.hub != nil {
				h.hub.SendTo(deviceID, b)
			}
		}
	}
}

func (h *Handler) listTerminalSessions(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	rows, err := h.repo.ListTerminalSessions(r.Context(), deviceID, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []TerminalSession{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func strPtr(s string) *string {
	return &s
}

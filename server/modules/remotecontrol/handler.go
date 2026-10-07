package remotecontrol

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

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
	relay          *RelayManager
	hub            Hub
	devices        *devicemgmt.Repository
	audit          AuditLogger
	authMiddleware func(http.Handler) http.Handler
	upgrader       websocket.Upgrader
}

func NewHandler(
	repo *Repository,
	relay *RelayManager,
	hub Hub,
	devices *devicemgmt.Repository,
	audit AuditLogger,
	authMiddleware func(http.Handler) http.Handler,
	checkOrigin auth.OriginChecker,
) *Handler {
	if checkOrigin == nil {
		checkOrigin = auth.ValidateWebSocketOrigin
	}
	return &Handler{
		repo:           repo,
		relay:          relay,
		hub:            hub,
		devices:        devices,
		audit:          audit,
		authMiddleware: authMiddleware,
		upgrader: websocket.Upgrader{
			CheckOrigin: checkOrigin,
		},
	}
}

func (h *Handler) Register(r chi.Router) {
	// 1. Session initialization and management (Technician minimum)
	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).
		Post("/api/devices/{id}/remotecontrol/session", h.startSession)

	r.With(h.authMiddleware, rbac.RequireRole(rbac.RoleTechnician)).
		Post("/api/devices/{id}/remotecontrol/sessions/{sessionId}/stop", h.stopSession)

	// 2. Session history (Viewer minimum)
	r.With(h.authMiddleware).
		Get("/api/devices/{id}/remotecontrol/sessions", h.listSessions)

	// 3. Technician Interactive WebSocket Relay (auth via query param token)
	r.Get("/api/devices/{id}/remotecontrol/ws", h.handleOperatorWS)

	// 4. Agent Streaming WebSocket Relay (auth via session ID check)
	r.Get("/api/agent/devices/{id}/remotecontrol/ws", h.handleAgentWS)
}

type startSessionReq struct {
	Mode string `json:"mode"` // 'full_control' or 'view_only'
}

func (h *Handler) startSession(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	if _, err := h.devices.GetByID(r.Context(), deviceID); err != nil {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}

	if h.hub == nil || !h.hub.Online(deviceID) {
		writeErr(w, http.StatusConflict, "device is offline")
		return
	}

	var req startSessionReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Mode == "" {
		req.Mode = "full_control"
	}
	if req.Mode != "full_control" && req.Mode != "view_only" {
		writeErr(w, http.StatusBadRequest, "invalid mode (must be full_control or view_only)")
		return
	}

	operatorID := auth.UserIDFromContext(r.Context())
	session := &RemoteControlSession{
		DeviceID:    deviceID,
		OperatorID:  operatorID,
		SessionMode: req.Mode,
	}

	if err := h.repo.CreateSession(r.Context(), session); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to create session: "+err.Error())
		return
	}

	// Register relay session
	h.relay.RegisterSession(session.ID, session.SessionMode)

	// Notify agent via WebSocket command
	cmdPayload := map[string]any{
		"session_id": session.ID,
		"mode":       session.SessionMode,
		"relay_url":  "/api/agent/devices/" + deviceID + "/remotecontrol/ws?session=" + session.ID,
	}
	env := transport.Envelope{
		Type:    transport.TypeCommand,
		ID:      session.ID,
		Command: "rc.start",
		Payload: cmdPayload,
	}
	envBytes, _ := json.Marshal(env)
	// The return value was discarded, so a dropped dispatch produced a session
	// that was announced as active and written to the database as active while
	// no agent had ever been told about it. SendTo returns false when the
	// device's 64-slot send queue is full, which happens on a busy link, and
	// nothing retries it: the session just sat in history as ACTIVE forever
	// next to a console showing an operator waiting for a desktop that never
	// opened.
	//
	// A session the agent never received cannot be repaired, so it is closed
	// where it stands rather than left claiming to be live. Ending it here also
	// means history shows what actually happened instead of a row that only a
	// human reading the logs could reinterpret.
	if !h.hub.SendTo(deviceID, envBytes) {
		h.relay.CloseRelay(session.ID)
		_ = h.repo.EndSession(r.Context(), session.ID, 0, 0, 0)
		_ = h.audit.Log(r.Context(), "user", operatorID, "remotecontrol.session_start_failed", session.ID,
			map[string]string{"device_id": deviceID, "reason": "agent send failed"})
		writeErr(w, http.StatusServiceUnavailable,
			"the agent's connection could not accept the session; it may be busy or reconnecting")
		return
	}

	_ = h.audit.Log(r.Context(), "user", operatorID, "remotecontrol.session_start", session.ID, map[string]string{
		"device_id": deviceID,
		"mode":      session.SessionMode,
	})

	writeJSON(w, http.StatusCreated, session)
}

func (h *Handler) stopSession(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	deviceID := chi.URLParam(r, "id")

	// Closing the relay is not enough. The agent's capture loop is a 10 fps
	// ticker that only stops when the socket it writes to fails or when it is
	// told to, and nothing told it: the server had no rc.stop dispatch at all,
	// so ending a session from the console severed the operator's side and left
	// the endpoint capturing and encoding a desktop into a closed socket for as
	// long as the agent kept running -- the CPU and the disk-free bandwidth of a
	// full-screen JPEG encode, ten times a second, for an operator who had
	// already left.
	//
	// The device id comes from the path, which is authenticated, and the relay
	// itself is closed unconditionally below, so a session id that does not
	// belong to this device still cannot be used to stop another device's
	// capture: the stop is addressed to a device, not to a session.
	if h.hub != nil {
		env := transport.Envelope{
			Type:    transport.TypeCommand,
			ID:      sessionID,
			Command: "rc.stop",
			Payload: map[string]any{"session_id": sessionID},
		}
		envBytes, err := json.Marshal(env)
		if err == nil {
			h.hub.SendTo(deviceID, envBytes)
		}
	}

	h.relay.CloseRelay(sessionID)

	operatorID := auth.UserIDFromContext(r.Context())
	_ = h.audit.Log(r.Context(), "user", operatorID, "remotecontrol.session_stop", sessionID, map[string]string{
		"device_id": deviceID,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ended"})
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}

	sessions, err := h.repo.ListSessionsByDevice(r.Context(), deviceID, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sessions == nil {
		sessions = []RemoteControlSession{}
	}
	writeJSON(w, http.StatusOK, sessions)
}

// authenticateHandshake decides who is opening a remote control socket and
// writes the refusal itself when the answer is no.
//
// A ticket is the only credential this socket accepts, for the same reason as on
// the terminal socket: a browser cannot attach an Authorization header to a
// WebSocket, and a credential in the query string is written to the access log,
// kept in history and forwarded in Referer.
//
// The ?token=<jwt> fallback is removed rather than left as a tolerated path. It
// used to accept a refresh token -- a week-long credential that opened a remote
// desktop and lived in every access log the proxy kept.
func (h *Handler) authenticateHandshake(w http.ResponseWriter, r *http.Request) (auth.Claims, bool) {
	const refuse = "unauthorized or insufficient privileges"

	ticket := auth.TicketFromRequest(r)
	if ticket == "" {
		http.Error(w, refuse, http.StatusForbidden)
		return auth.Claims{}, false
	}
	userID, ok := auth.RedeemWebSocketTicket(r.Context(), ticket, auth.PurposeRemoteDesktop)
	if !ok {
		http.Error(w, refuse, http.StatusForbidden)
		return auth.Claims{}, false
	}
	claims, ok := auth.ClaimsForUser(r.Context(), auth.CurrentRoleReader(), userID)
	if !ok || claims.Role == rbac.RoleViewer {
		http.Error(w, refuse, http.StatusForbidden)
		return auth.Claims{}, false
	}
	return claims, true
}

func (h *Handler) handleOperatorWS(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		http.Error(w, "missing session", http.StatusUnauthorized)
		return
	}

	// The claims are not used past this point: the relay identifies the operator
	// by session id, and the role check has already happened. Binding the
	// operator to the authenticated user is left to the session record, which
	// startSession created, rather than reconstructed here.
	if _, ok := h.authenticateHandshake(w, r); !ok {
		return // the refusal is already written
	}

	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("upgrade operator remote control ws")
		return
	}
	defer ws.Close()

	relay, ok := h.relay.AttachOperator(sessionID, ws)
	if !ok {
		_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "failed to attach to relay"))
		return
	}

	if relay.IsAgentClosed() {
		time.Sleep(50 * time.Millisecond)
		_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"))
		h.relay.CloseRelay(sessionID)
		return
	}

	// Read operator input events and forward to agent
	for {
		msgType, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}

		// A mode change is handled by the relay, not treated as input. It has
		// to bypass the view_only gate in both directions: an operator sitting
		// in view-only must still be able to hand control back, otherwise the
		// toggle is a one-way door. Forwarding it to the agent as well keeps
		// the agent's own gate in step with the relay's.
		if mode, isMode := parseModeMessage(msg); isMode {
			if !h.relay.SetMode(sessionID, mode) {
				continue
			}
			_ = relay.ForwardControlMessage(msgType, msg)
			continue
		}

		if err := relay.ForwardOperatorInput(msgType, msg); err != nil {
			break
		}
	}

	h.relay.CloseRelay(sessionID)
}

func (h *Handler) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		http.Error(w, "missing session param", http.StatusBadRequest)
		return
	}

	// Authenticate BEFORE the upgrade. Once the socket is hijacked there is no
	// second chance to reject the peer, and a knowledge of a session ID alone
	// would otherwise be enough to inject screen frames into an operator's view.
	authenticatedDeviceID, ok := devicemgmt.AuthenticateAgent(w, r, h.devices)
	if !ok {
		return
	}

	// The device secret proves *which* agent is calling, not that this agent
	// belongs to this session. Without the ownership check below, any enrolled
	// device holding its own valid secret could attach to another device's
	// session by guessing or leaking a session ID, and push frames into an
	// operator's view of a third machine.
	session, err := h.repo.GetSessionByID(r.Context(), sessionID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}
	if session.DeviceID != authenticatedDeviceID {
		log.Warn().
			Str("session", sessionID).
			Str("session_device", session.DeviceID).
			Str("auth_device", authenticatedDeviceID).
			Msg("rejected remote control agent attach: device does not own the session")
		writeErr(w, http.StatusForbidden, "this device does not own the requested session")
		return
	}

	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("upgrade agent remote control ws")
		return
	}
	defer ws.Close()

	relay, ok := h.relay.AttachAgent(sessionID, ws)
	if !ok {
		_ = ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "failed to attach to relay"))
		return
	}

	// A retired or rotated device must not keep streaming on a socket that
	// authenticated before the credential changed. Check again after attach to
	// cover the handshake race, and before forwarding every subsequent frame.
	secretHash := devicemgmt.HashToken(r.Header.Get("X-Device-Secret"))
	credentialLive := func() bool {
		dev, err := h.devices.FindBySecretHash(r.Context(), secretHash)
		return err == nil && dev.ID == authenticatedDeviceID
	}
	if !credentialLive() {
		h.relay.CloseRelay(sessionID)
		return
	}
	for {
		msgType, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if !credentialLive() {
			h.relay.CloseRelay(sessionID)
			break
		}
		if err := relay.ForwardAgentFrame(msgType, msg); err != nil {
			break
		}
	}

	h.relay.AgentDisconnected(sessionID)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// parseModeMessage reports whether a console message is a mode change, and to
// which mode. A malformed message is not a mode change: it falls through to the
// input path, where the agent's JSON decode drops it like any other bad frame.
func parseModeMessage(msg []byte) (string, bool) {
	var probe struct {
		Type string `json:"type"`
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(msg, &probe); err != nil {
		return "", false
	}
	if probe.Type != "mode" {
		return "", false
	}
	if probe.Mode != "full_control" && probe.Mode != "view_only" {
		return "", false
	}
	return probe.Mode, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

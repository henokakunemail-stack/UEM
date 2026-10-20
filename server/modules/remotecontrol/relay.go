package remotecontrol

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

const (
	operatorWriteWait = 10 * time.Second
	agentWriteWait    = 10 * time.Second
	operatorPongWait  = 60 * time.Second
	agentPongWait     = 60 * time.Second

	// attachTimeout is how long a registered relay waits for its first socket.
	// rc.start is fire-and-forget, so an agent that received it and never
	// dialled, with an operator who never opened the console, would otherwise
	// hold the relay and an 'active' row until the server restarted.
	attachTimeout = 2 * time.Minute
)

type activeRelay struct {
	sessionID string
	mode      string

	// Frames and input have separate write mutexes. Sharing one lock meant a
	// ~100KB screen frame and a keypress queued behind each other, so every
	// keystroke waited on the current frame to finish writing: the visible
	// symptom was an operator typing into a responsive-looking desktop and
	// having the characters arrive in a visible batch, late, after the cursor
	// had already moved on.
	operatorWriteMu sync.Mutex
	operatorWS      *websocket.Conn
	agentWriteMu    sync.Mutex
	agentWS         *websocket.Conn

	mu             sync.Mutex
	framesCount    int
	bytesCount     int64
	inputsCount    int
	closed         bool
	closeChan      chan struct{}
	pendingControl [][]byte
	agentClosed    bool
}

type RelayManager struct {
	repo     *Repository
	relays   map[string]*activeRelay
	relaysMu sync.RWMutex

	// attachTimeout is a field so tests can shorten it.
	attachTimeout time.Duration
}

func NewRelayManager(repo *Repository) *RelayManager {
	return &RelayManager{
		repo:          repo,
		relays:        make(map[string]*activeRelay),
		attachTimeout: attachTimeout,
	}
}

func (rm *RelayManager) RegisterSession(sessionID, mode string) *activeRelay {
	rm.relaysMu.Lock()
	defer rm.relaysMu.Unlock()

	r := &activeRelay{
		sessionID: sessionID,
		mode:      mode,
		closeChan: make(chan struct{}),
	}
	rm.relays[sessionID] = r
	go rm.expireUnattached(r, rm.attachTimeout)
	return r
}

// expireUnattached closes a relay that no agent and no operator ever attached
// to within the timeout, which also ends its session row.
func (rm *RelayManager) expireUnattached(r *activeRelay, timeout time.Duration) {
	select {
	case <-r.closeChan:
		return
	case <-time.After(timeout):
	}
	rm.relaysMu.RLock()
	current := rm.relays[r.sessionID] == r
	rm.relaysMu.RUnlock()
	r.mu.Lock()
	attached := r.agentWS != nil || r.operatorWS != nil
	r.mu.Unlock()
	if current && !attached {
		log.Info().Str("session", r.sessionID).Msg("remote control relay expired: nothing attached")
		rm.CloseRelay(r.sessionID)
	}
}

// relayForAttach returns the relay a socket should join, creating it when it is
// missing. A recreated relay takes its mode from the session row the handler
// read, not a default: the old hard-coded full_control turned a view_only
// session into a controllable one whenever its relay had to be rebuilt. A relay
// that is closing is refused rather than revived.
func (rm *RelayManager) relayForAttach(sessionID, mode string) (*activeRelay, bool) {
	rm.relaysMu.Lock()
	defer rm.relaysMu.Unlock()
	r, exists := rm.relays[sessionID]
	if !exists {
		if mode != "view_only" {
			mode = "full_control"
		}
		r = &activeRelay{
			sessionID: sessionID,
			mode:      mode,
			closeChan: make(chan struct{}),
		}
		rm.relays[sessionID] = r
		return r, true
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	return r, !closed
}

func (rm *RelayManager) GetRelay(sessionID string) *activeRelay {
	rm.relaysMu.RLock()
	defer rm.relaysMu.RUnlock()
	return rm.relays[sessionID]
}

// startKeepalive pings one relay socket on a fixed period so the peer answers
// with a pong, which is the only thing that refreshes the read deadline set in
// AttachOperator and AttachAgent.
//
// Without it that deadline was an absolute kill switch rather than an idle
// timeout. A pong is only ever sent in reply to a ping, so with no ping loop the
// PongHandler was unreachable and every remote-control session died at exactly
// operatorPongWait after attaching -- sixty seconds of live desktop, then an
// abrupt close, with nothing in the logs to say why. The keepalive period is a
// third of the deadline, so one lost pong does not end a session in use.
//
// The function returns when the ping fails or the relay closes; the caller's
// read loop is still blocked and is what ends the session.
func startKeepalive(ws *websocket.Conn, closeChan <-chan struct{}, period time.Duration) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-closeChan:
			return
		case <-ticker.C:
			if err := ws.WriteControl(websocket.PingMessage, nil,
				time.Now().Add(operatorWriteWait)); err != nil {
				return
			}
		}
	}
}

// AttachOperator joins the operator socket to the session's relay. mode is the
// session row's mode and is used only if the relay has to be recreated.
func (rm *RelayManager) AttachOperator(sessionID, mode string, ws *websocket.Conn) (*activeRelay, bool) {
	r, ok := rm.relayForAttach(sessionID, mode)
	if !ok {
		return nil, false
	}

	r.mu.Lock()
	r.operatorWS = ws
	pending := r.pendingControl
	r.pendingControl = nil
	r.mu.Unlock()

	if len(pending) > 0 {
		r.operatorWriteMu.Lock()
		for _, msg := range pending {
			_ = ws.WriteMessage(websocket.TextMessage, msg)
		}
		r.operatorWriteMu.Unlock()
	}

	// A read deadline plus a pong handler keeps a half-open socket — a laptop
	// that closed without a close frame — from holding the relay open forever.
	// The ping loop below is what makes the pong handler reachable.
	ws.SetReadDeadline(time.Now().Add(operatorPongWait))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(operatorPongWait))
	})
	go startKeepalive(ws, r.closeChan, operatorPongWait/3)

	return r, true
}

// AttachAgent joins the agent socket to the session's relay. mode is the
// session row's mode and is used only if the relay has to be recreated.
func (rm *RelayManager) AttachAgent(sessionID, mode string, ws *websocket.Conn) (*activeRelay, bool) {
	r, ok := rm.relayForAttach(sessionID, mode)
	if !ok {
		return nil, false
	}

	r.mu.Lock()
	r.agentWS = ws
	r.mu.Unlock()

	ws.SetReadDeadline(time.Now().Add(agentPongWait))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(agentPongWait))
	})
	go startKeepalive(ws, r.closeChan, agentPongWait/3)

	return r, true
}

func (rm *RelayManager) CloseRelay(sessionID string) {
	rm.relaysMu.RLock()
	r, exists := rm.relays[sessionID]
	rm.relaysMu.RUnlock()

	// The durable row is closed whether or not a relay object was ever
	// registered. EndSession used to sit below the `r == nil` early return, so
	// ending a session that never got as far as having a relay -- the operator
	// closed the tab before the agent attached, or the dispatch failed
	// outright -- left the row in 'active' with a NULL ended_at permanently.
	// Nothing sweeps it, so it stayed in the device's session history as an
	// ACTIVE session for the life of the installation, indistinguishable from
	// one an operator has open right now.
	//
	// But a relay that is already gone may well have recorded telemetry before
	// it left: the operator's read loop ends the relay itself when its socket
	// breaks, the map entry is deleted there, and stopSession then arrives and
	// takes this branch. Writing zeros through EndSession on a row that is still
	// 'active' at that moment replaces real counts with nothing -- the session
	// history shows zero frames for a desktop the operator was watching. The
	// 'ended' status is what this branch owns; the counters belong to the relay
	// that closed them, so it reports only the status.
	//
	// The two closes race, and the other ordering used to be just as bad: this
	// branch landed first, took the row to 'ended', and the relay-present close
	// arrived later with the counters it snapshotted. The relay-present path now
	// keeps its map entry until EndSession has written, so a concurrent close
	// finds the relay, sees it closing and returns; this branch is reached only
	// once the counters are already on the row. That is what lets EndSession be
	// limited to 'active' rows, which keeps a relay recreated after the session
	// ended from overwriting the finished row.
	if !exists || r == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = rm.repo.CloseSession(ctx, sessionID)
		return
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	close(r.closeChan)
	operatorWS, agentWS := r.operatorWS, r.agentWS
	frames := r.framesCount
	totalBytes := r.bytesCount
	inputs := r.inputsCount
	r.mu.Unlock()

	if operatorWS != nil {
		_ = operatorWS.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"),
			time.Now().Add(operatorWriteWait))
		_ = operatorWS.Close()
	}
	if agentWS != nil {
		_ = agentWS.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"),
			time.Now().Add(agentWriteWait))
		_ = agentWS.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = rm.repo.EndSession(ctx, sessionID, frames, totalBytes, inputs)

	// Released only after the counters are written; see the comment above.
	rm.relaysMu.Lock()
	if rm.relays[sessionID] == r {
		delete(rm.relays, sessionID)
	}
	rm.relaysMu.Unlock()
	log.Info().Str("session", sessionID).Int("frames", frames).Int("inputs", inputs).Msg("remote control relay closed")
}

// AgentDisconnected handles an agent websocket disconnect. If the operator is already
// attached, the relay closes immediately. If the operator has not yet completed the
// HTTP/WS ticket handshake, the relay remains accessible for a 15-second grace period
// so the operator receives buffered terminal messages (such as "unsupported").
func (rm *RelayManager) AgentDisconnected(sessionID string) {
	rm.relaysMu.Lock()
	r, exists := rm.relays[sessionID]
	if !exists {
		rm.relaysMu.Unlock()
		return
	}
	r.mu.Lock()
	if r.operatorWS != nil {
		r.mu.Unlock()
		rm.relaysMu.Unlock()
		rm.CloseRelay(sessionID)
		return
	}
	r.agentClosed = true
	r.mu.Unlock()
	rm.relaysMu.Unlock()

	go func() {
		select {
		case <-r.closeChan:
			return
		case <-time.After(15 * time.Second):
			rm.CloseRelay(sessionID)
		}
	}()
}

// IsAgentClosed reports whether the agent has already disconnected from the relay.
func (r *activeRelay) IsAgentClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agentClosed
}

// ForwardControlMessage sends a control message to the agent regardless of the
// input gate. It is the mode-change path, which must work in both directions.
func (r *activeRelay) ForwardControlMessage(msgType int, data []byte) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	ws := r.agentWS
	r.mu.Unlock()

	if ws == nil {
		return nil
	}
	r.agentWriteMu.Lock()
	defer r.agentWriteMu.Unlock()
	return ws.WriteMessage(msgType, data)
}

// SetMode changes the relay's input gate without ending the session. The console
// uses this for the Control/View toggle: previously the mode lived only in the
// session row, so switching it meant starting a new session, which dropped the
// live stream and lost the operator's place.
func (rm *RelayManager) SetMode(sessionID, mode string) bool {
	if mode != "full_control" && mode != "view_only" {
		return false
	}
	rm.relaysMu.RLock()
	r, ok := rm.relays[sessionID]
	rm.relaysMu.RUnlock()
	if !ok {
		return false
	}
	r.mu.Lock()
	r.mode = mode
	r.mu.Unlock()
	return true
}

// CurrentMode reports the relay's input gate. Exported so the mode contract is
// assertable from another package rather than only from inside this one.
func (r *activeRelay) CurrentMode() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mode
}

// ForwardAgentFrame forwards a screen frame from the agent to the operator.
func (r *activeRelay) ForwardAgentFrame(msgType int, data []byte) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	// Counted before the operator check, which is where it always was. The
	// pending-control buffer below is new, but moving these two lines under it
	// meant a frame the agent produced while no operator was attached vanished
	// from the session telemetry entirely -- so the worse the delay between
	// starting a session and opening the console, the less the record showed.
	// The counter answers "how much did this endpoint send", which is a fact
	// about the endpoint and does not depend on anyone being watching.
	r.framesCount++
	r.bytesCount += int64(len(data))

	ws := r.operatorWS
	if ws == nil {
		if msgType == websocket.TextMessage {
			msgCopy := make([]byte, len(data))
			copy(msgCopy, data)
			r.pendingControl = append(r.pendingControl, msgCopy)
		}
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	r.operatorWriteMu.Lock()
	defer r.operatorWriteMu.Unlock()
	_ = ws.SetWriteDeadline(time.Now().Add(2500 * time.Millisecond))
	return ws.WriteMessage(msgType, data)
}

// ForwardOperatorInput forwards an operator input event to the agent. In
// view_only mode the event is dropped and not counted, so the session telemetry
// records only input that actually reached the endpoint.
func (r *activeRelay) ForwardOperatorInput(msgType int, data []byte) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	if r.mode == "view_only" {
		r.mu.Unlock()
		return nil
	}
	ws := r.agentWS
	r.inputsCount++
	r.mu.Unlock()

	if ws == nil {
		return nil
	}
	r.agentWriteMu.Lock()
	defer r.agentWriteMu.Unlock()
	return ws.WriteMessage(msgType, data)
}

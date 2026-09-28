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

	mu          sync.Mutex
	framesCount int
	bytesCount  int64
	inputsCount int
	closed      bool
	closeChan   chan struct{}
}

type RelayManager struct {
	repo     *Repository
	relays   map[string]*activeRelay
	relaysMu sync.RWMutex
}

func NewRelayManager(repo *Repository) *RelayManager {
	return &RelayManager{
		repo:   repo,
		relays: make(map[string]*activeRelay),
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
	return r
}

func (rm *RelayManager) GetRelay(sessionID string) *activeRelay {
	rm.relaysMu.RLock()
	defer rm.relaysMu.RUnlock()
	return rm.relays[sessionID]
}

func (rm *RelayManager) AttachOperator(sessionID string, ws *websocket.Conn) (*activeRelay, bool) {
	rm.relaysMu.Lock()
	r, exists := rm.relays[sessionID]
	if !exists {
		r = &activeRelay{
			sessionID: sessionID,
			mode:      "full_control",
			closeChan: make(chan struct{}),
		}
		rm.relays[sessionID] = r
	}
	rm.relaysMu.Unlock()

	r.mu.Lock()
	r.operatorWS = ws
	r.mu.Unlock()

	// A read deadline plus a pong handler keeps a half-open socket — a laptop
	// that closed without a close frame — from holding the relay open forever.
	ws.SetReadDeadline(time.Now().Add(operatorPongWait))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(operatorPongWait))
	})

	return r, true
}

func (rm *RelayManager) AttachAgent(sessionID string, ws *websocket.Conn) (*activeRelay, bool) {
	rm.relaysMu.Lock()
	r, exists := rm.relays[sessionID]
	if !exists {
		r = &activeRelay{
			sessionID: sessionID,
			mode:      "full_control",
			closeChan: make(chan struct{}),
		}
		rm.relays[sessionID] = r
	}
	rm.relaysMu.Unlock()

	r.mu.Lock()
	r.agentWS = ws
	r.mu.Unlock()

	ws.SetReadDeadline(time.Now().Add(agentPongWait))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(agentPongWait))
	})

	return r, true
}

func (rm *RelayManager) CloseRelay(sessionID string) {
	rm.relaysMu.Lock()
	r, exists := rm.relays[sessionID]
	if exists {
		delete(rm.relays, sessionID)
	}
	rm.relaysMu.Unlock()

	if !exists || r == nil {
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
	log.Info().Str("session", sessionID).Int("frames", frames).Int("inputs", inputs).Msg("remote control relay closed")
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
	ws := r.operatorWS
	r.framesCount++
	r.bytesCount += int64(len(data))
	r.mu.Unlock()

	if ws == nil {
		return nil
	}
	r.operatorWriteMu.Lock()
	defer r.operatorWriteMu.Unlock()
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

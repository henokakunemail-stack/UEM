package remoteexec

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

// ActiveTerminalSession holds the active browser WebSocket for an operator.
type ActiveTerminalSession struct {
	SessionID string
	DeviceID  string
	UserID    string
	ws        *websocket.Conn
	writeMu   sync.Mutex
	closed    bool
}

// TerminalRelay manages live browser-to-agent terminal sessions.
// It implements transport.TerminalReceiver.
type TerminalRelay struct {
	mu       sync.RWMutex
	sessions map[string]*ActiveTerminalSession
}

func NewTerminalRelay() *TerminalRelay {
	return &TerminalRelay{
		sessions: make(map[string]*ActiveTerminalSession),
	}
}

// Register registers an active browser terminal session.
func (r *TerminalRelay) Register(sessionID, deviceID, userID string, ws *websocket.Conn) *ActiveTerminalSession {
	sess := &ActiveTerminalSession{
		SessionID: sessionID,
		DeviceID:  deviceID,
		UserID:    userID,
		ws:        ws,
	}
	r.mu.Lock()
	r.sessions[sessionID] = sess
	r.mu.Unlock()
	return sess
}

// Unregister removes a session.
func (r *TerminalRelay) Unregister(sessionID string) {
	r.mu.Lock()
	sess, ok := r.sessions[sessionID]
	delete(r.sessions, sessionID)
	r.mu.Unlock()
	if ok {
		// Outside r.mu, and under the session's own lock: closed is read by
		// WriteToBrowser under writeMu, so it is written under writeMu too.
		sess.markClosed()
	}
}

// Get returns the active session if registered.
func (r *TerminalRelay) Get(sessionID string) *ActiveTerminalSession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions[sessionID]
}

// markClosed retires the session under the same lock that WriteToBrowser reads
// it under.
//
// closed used to be assigned directly in Unregister, which holds r.mu, while
// WriteToBrowser tests it while holding s.writeMu. The two are different locks,
// so the field had no synchronisation at all between them: the agent-connection
// goroutine calls AcceptTerminalData -> WriteToBrowser, and the browser handler
// calls Unregister from its defer, and on a shell closing the operator has just
// navigated away, which is exactly when the two run at once. Go's memory model
// gives no ordering between the write and the read, so a caller could be handed
// a stale false and write into a socket the handler is concurrently tearing
// down.
//
// The pointer handed out by Get is likewise not invalidated by Unregister -- the
// session is removed from the map but the object lives on for whoever already
// holds it -- so marking closed is the only thing that stops a late write.
func (s *ActiveTerminalSession) markClosed() {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.closed = true
}

// WriteToBrowser sends a terminal message to the browser WebSocket.
func (s *ActiveTerminalSession) WriteToBrowser(msgType string, data string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed || s.ws == nil {
		return errors.New("terminal session closed")
	}
	payload := map[string]string{
		"type":       msgType,
		"session_id": s.SessionID,
		"data":       data,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_ = s.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return s.ws.WriteMessage(websocket.TextMessage, b)
}

// AcceptTerminalData forwards agent terminal stdout data to the operator's browser.
func (r *TerminalRelay) AcceptTerminalData(ctx context.Context, deviceID string, sessionID string, data string) error {
	sess := r.Get(sessionID)
	if sess == nil {
		log.Debug().Str("session_id", sessionID).Msg("terminal data for unknown or closed session")
		return nil
	}
	return sess.WriteToBrowser("term.data", data)
}

// AcceptTerminalClose handles closure initiated by the agent.
func (r *TerminalRelay) AcceptTerminalClose(ctx context.Context, deviceID string, sessionID string) error {
	sess := r.Get(sessionID)
	if sess == nil {
		return nil
	}
	_ = sess.WriteToBrowser("term.close", "Shell process terminated by remote host.")
	r.Unregister(sessionID)
	return nil
}

package remotecontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

type SessionConfig struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode"`
	RelayURL  string `json:"relay_url"`
}

// Credentials are the agent's per-device secret, the same pair every other
// agent module sends. The relay authenticates the agent-side socket with them,
// so an unauthenticated peer cannot push frames into an operator's session.
type Credentials struct {
	DeviceID     string
	DeviceSecret string
}

// Capabilities tells the operator what this endpoint can actually do before the
// session starts looking live. Every platform reports honestly: a capturer that
// cannot grab a real framebuffer says so here instead of streaming a synthetic
// image, and the console shows the reason rather than a black canvas.
type Capabilities struct {
	Capture  bool   `json:"capture"`
	Mouse    bool   `json:"mouse"`
	Keyboard bool   `json:"keyboard"`
	Reason   string `json:"reason,omitempty"`
}

// InputEvent is the operator's intent, decoded from the console. One struct for
// every input kind keeps the wire format and the capturer signatures in step:
// adding a field cannot silently desynchronise the two.
type InputEvent struct {
	Type   string `json:"type"`   // "mouse", "keyboard" or "mode"
	Action string `json:"action"` // move/down/up/wheel, down/up, set, release
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	Button string `json:"button,omitempty"`
	Delta  int    `json:"delta,omitempty"`
	// Code is the browser's legacy KeyboardEvent.keyCode, which on Windows is
	// the virtual-key code. It is what the platform layer translates into a scan
	// code, because a scan code is the only thing SendInput accepts that
	// distinguishes left/right modifiers and keypad keys.
	Code int    `json:"code,omitempty"`
	Key  string `json:"key,omitempty"`
	Mode string `json:"mode,omitempty"`
}

// ScreenCapturer produces a frame, not an encoded image.
//
// The return is raw BGRA pixels rather than a JPEG because the change detection
// has to happen before any encoding. Diffing encoded frames is possible and
// useless: a JPEG of a desktop that changed in one corner differs across the
// whole image because the encoder's block boundaries moved, so every frame
// looks wholly damaged and the transmission stays full-screen. Diffing pixels
// finds the corner.
type ScreenCapturer interface {
	Capabilities() Capabilities
	// CaptureScreen returns one frame as BGRA pixels, 4 bytes per pixel, row by
	// row, with no padding between rows.
	CaptureScreen() ([]byte, int, int, error)
	InjectMouseEvent(e InputEvent) error
	InjectKeyboardEvent(e InputEvent) error
	// ReleaseAllKeys lifts every key the session believes is held. Without it a
	// keyup lost to a focus change leaves the key stuck down on the endpoint —
	// a held Ctrl or Win key is a workstation-wide problem, not a cosmetic one.
	ReleaseAllKeys() error
}

// Frame rate the agent captures at.
//
// This is the rate at which the endpoint is sampled, not the rate at which the
// operator sees something change. An unchanged frame produces no rectangles and
// no network traffic at all, so the visible frame rate on a mostly-static
// desktop is far below this while the cost stays the same as before. 10 fps is
// the point where a cursor movement or a scroll still reads as continuous; a
// higher rate only adds blits on the endpoint, which is CPU the endpoint's owner
// is paying for and cannot see the benefit of.
const captureInterval = 100 * time.Millisecond

type Session struct {
	config    SessionConfig
	serverURL string
	creds     Credentials
	capturer  ScreenCapturer
	conn      *websocket.Conn
	mu        sync.Mutex
	stopChan  chan struct{}
	stopOnce  sync.Once
	modeMu    sync.RWMutex
	mode      string
}

func NewSession(cfg SessionConfig, serverBaseURL string, creds Credentials, capturer ScreenCapturer) *Session {
	mode := cfg.Mode
	if mode == "" {
		mode = "full_control"
	}
	return &Session{
		config:    SessionConfig{SessionID: cfg.SessionID, Mode: mode, RelayURL: cfg.RelayURL},
		serverURL: serverBaseURL,
		creds:     creds,
		capturer:  capturer,
		stopChan:  make(chan struct{}),
		mode:      mode,
	}
}

func (s *Session) Start(ctx context.Context) error {
	u, err := url.Parse(s.serverURL)
	if err != nil {
		return fmt.Errorf("parse server url: %w", err)
	}

	wsScheme := "ws"
	if u.Scheme == "https" {
		wsScheme = "wss"
	}
	wsURL := fmt.Sprintf("%s://%s%s", wsScheme, u.Host, s.config.RelayURL)

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, http.Header{
		"User-Agent":      []string{"EndpointMgmt-Agent-RC/1.0"},
		"X-Device-Id":     []string{s.creds.DeviceID},
		"X-Device-Secret": []string{s.creds.DeviceSecret},
	})
	if err != nil {
		return fmt.Errorf("dial rc relay ws: %w", err)
	}
	s.conn = conn

	log.Info().Str("session", s.config.SessionID).Str("mode", s.mode).Msg("remote control agent session connected")

	// Handshake first, on the same socket the frames use. The relay is a dumb
	// pipe, so this reaches the console without a new endpoint, a protocol
	// change or a database column — and it tells the operator what this endpoint
	// can do before the first frame is due.
	if err := s.sendControl(map[string]any{
		"type":         "hello",
		"session_id":   s.config.SessionID,
		"mode":         s.mode,
		"os":           runtime.GOOS,
		"capabilities": s.capturer.Capabilities(),
	}); err != nil {
		// The dial succeeded, so this socket is now open and owned by nobody:
		// the goroutines below were never started, so nothing else will close
		// it. Returning without this leaks one connection per retry, and the
		// relay sees a stream of sessions that connect and vanish.
		s.mu.Lock()
		_ = conn.Close()
		s.conn = nil
		s.mu.Unlock()
		return fmt.Errorf("send rc hello: %w", err)
	}

	go s.readInputLoop()
	go s.streamFramesLoop()

	return nil
}

func (s *Session) Stop() {
	s.stopOnce.Do(func() {
		// Lift held keys before the socket goes away. A session that ends while
		// the operator is holding Ctrl must not leave it held on the endpoint.
		_ = s.capturer.ReleaseAllKeys()

		// The close frame is a write like any other and has to take the same
		// lock. gorilla/websocket permits exactly one concurrent writer: two
		// interleave their frame headers and corrupt both, so an operator
		// clicking "End session" while a frame was in flight could splice a
		// close opcode into the middle of a payload. The session then neither
		// ends nor updates, with no error on either side.
		s.mu.Lock()
		conn := s.conn
		if conn != nil {
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session closed by agent"))
			_ = conn.Close()
		}
		s.mu.Unlock()
		close(s.stopChan)
	})
}

// ID reports the session this object represents, so a stop command can name the
// session it means instead of whatever happened to be running.
func (s *Session) ID() string { return s.config.SessionID }

// Wait blocks until the session has ended, and is what keeps a process that
// owns a session alive to own it.
//
// Start returns as soon as the socket is up and the loops are running, so a
// caller that treated Start as the whole job would end the session the instant
// it began. Only Stop closes the channel: the streaming loop watches stopChan,
// and every path that ends a session — an operator stopping it, the socket
// dying, a context cancellation — has to come through Stop for the wait to
// release.
func (s *Session) Wait(ctx context.Context) {
	select {
	case <-s.stopChan:
	case <-ctx.Done():
		s.Stop()
	}
}

func (s *Session) currentMode() string {
	s.modeMu.RLock()
	defer s.modeMu.RUnlock()
	return s.mode
}

func (s *Session) setMode(m string) {
	if m != "full_control" && m != "view_only" {
		return
	}
	s.modeMu.Lock()
	s.mode = m
	s.modeMu.Unlock()
	if m == "view_only" {
		_ = s.capturer.ReleaseAllKeys()
	}
}

func (s *Session) sendControl(v map[string]any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return fmt.Errorf("session not connected")
	}
	return s.conn.WriteMessage(websocket.TextMessage, b)
}

// SendNotice pushes a one-way control message to the console, for the cases
// where the failure happens before or instead of the frame stream.
//
// The stream loop already says "unsupported" when capture is impossible, but a
// dial that never completes and a socket that drops during the handshake have no
// frame to hide behind: the console would sit on an open relay showing nothing,
// which is the exact state this module is supposed to rule out. notice is the
// message type the console keys on; detail is the sentence it puts in front of
// the operator.
func (s *Session) SendNotice(notice, detail string) {
	_ = s.sendControl(map[string]any{
		"type":   notice,
		"reason": detail,
	})
}

func (s *Session) readInputLoop() {
	defer s.Stop()

	for {
		_, msg, err := s.conn.ReadMessage()
		if err != nil {
			break
		}

		var in InputEvent
		if err := json.Unmarshal(msg, &in); err != nil {
			continue
		}

		// A mode change is a control message, not input: it is honoured even in
		// view_only, otherwise the operator could never hand control back.
		if in.Type == "mode" {
			s.setMode(in.Mode)
			continue
		}

		if s.currentMode() == "view_only" {
			continue // drop input in view_only mode
		}

		switch in.Type {
		case "mouse":
			_ = s.capturer.InjectMouseEvent(in)
		case "keyboard":
			// A 'release' action carries no key: it means "lift everything you
			// think is held", which is what the console sends on blur.
			if in.Action == "release" {
				_ = s.capturer.ReleaseAllKeys()
			} else {
				_ = s.capturer.InjectKeyboardEvent(in)
			}
		}
	}
}

func (s *Session) streamFramesLoop() {
	defer s.Stop()

	ticker := time.NewTicker(captureInterval)
	defer ticker.Stop()

	caps := s.capturer.Capabilities()
	if !caps.Capture {
		// Say so on the wire and stop, rather than streaming a placeholder that
		// looks like a live desktop but is not one.
		_ = s.sendControl(map[string]any{
			"type":   "unsupported",
			"reason": caps.Reason,
		})
		log.Warn().Str("session", s.config.SessionID).Str("reason", caps.Reason).Msg("remote control capture unavailable on this platform")
		return
	}

	damage := NewDamage()
	// captureFailures counts consecutive blit failures so a repeat is reported
	// once rather than on every tick.
	captureFailures := 0
	// idleTicks counts consecutive frames that found nothing to send. It is only
	// used for the report below.
	idleTicks := 0

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			pixels, width, height, err := s.capturer.CaptureScreen()
			if err != nil {
				// A single blit can fail on a moment when the desktop is being
				// switched, and the next tick recovers. Looping forever in silence
				// is the other outcome: the relay stays open, the console stays
				// black, and nothing anywhere says the endpoint is in a state
				// where it cannot be captured. Three in a row is not a blip.
				//
				// Every third failure reports, not just the third ever. The
				// counter is only reset by a success, so `== 3` fired once for
				// the whole session and a capture that was broken from the start
				// and stayed broken told the console exactly one time -- long
				// enough ago that an operator watching later saw a silent black
				// canvas. A persistent fault keeps saying so.
				captureFailures++
				if captureFailures%3 == 0 {
					s.SendNotice("capture_failed", err.Error())
				}
				log.Debug().Err(err).Msg("capture screen error")
				continue
			}
			captureFailures = 0
			if len(pixels) == 0 {
				continue
			}

			damage.SetSize(width, height)
			rects := damage.Diff(pixels)
			if len(rects) == 0 {
				// Nothing on the endpoint moved. Sending an empty frame would
				// cost a websocket message and a console repaint for no visible
				// change, which on a static desktop is the overwhelming majority
				// of ticks.
				idleTicks++
				continue
			}
			if idleTicks > 0 {
				log.Debug().Int("idle_ticks", idleTicks).Msg("remote control desktop was static")
				idleTicks = 0
			}

			payload, err := EncodeFrame(pixels, width, height, rects)
			if err != nil {
				log.Error().Err(err).Msg("encode remote control frame")
				continue
			}

			s.mu.Lock()
			err = s.conn.WriteMessage(websocket.BinaryMessage, payload)
			s.mu.Unlock()
			if err != nil {
				return
			}

			// Only now is the baseline safe to advance. Doing it before the
			// write marks the region as delivered on a frame that never left
			// the agent, and the console then shows the old image there until
			// the session ends, with nothing in any log to explain it.
			damage.Acknowledge(rects)
		}
	}
}

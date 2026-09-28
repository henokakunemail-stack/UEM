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

// Frame rate the agent captures at. The relay is a pipe, so a higher value only
// costs bandwidth and CPU; 10 fps is the point where a desktop still reads as
// live rather than as a slideshow.
const captureInterval = 100 * time.Millisecond

type ScreenCapturer interface {
	Capabilities() Capabilities
	CaptureScreen() ([]byte, int, int, error)
	InjectMouseEvent(e InputEvent) error
	InjectKeyboardEvent(e InputEvent) error
	// ReleaseAllKeys lifts every key the session believes is held. Without it a
	// keyup lost to a focus change leaves the key stuck down on the endpoint —
	// a held Ctrl or Win key is a workstation-wide problem, not a cosmetic one.
	ReleaseAllKeys() error
}

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

		s.mu.Lock()
		conn := s.conn
		s.mu.Unlock()
		if conn != nil {
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session closed by agent"))
			_ = conn.Close()
		}
		close(s.stopChan)
	})
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

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			frameData, width, height, err := s.capturer.CaptureScreen()
			if err != nil {
				log.Debug().Err(err).Msg("capture screen error")
				continue
			}
			if len(frameData) == 0 {
				continue
			}

			// Frame header: width (2 bytes) + height (2 bytes), big endian, then
			// the encoded frame. The header is what lets the console resize its
			// canvas to the endpoint's real resolution instead of a fixed guess.
			payload := make([]byte, 4+len(frameData))
			payload[0] = byte(width >> 8)
			payload[1] = byte(width)
			payload[2] = byte(height >> 8)
			payload[3] = byte(height)
			copy(payload[4:], frameData)

			s.mu.Lock()
			err = s.conn.WriteMessage(websocket.BinaryMessage, payload)
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

package remotecontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/transport"
)

type SessionConfig struct {
	SessionID    string `json:"session_id"`
	Mode         string `json:"mode"`
	RelayURL     string `json:"relay_url"`
	DeviceID     string `json:"device_id,omitempty"`
	DeviceSecret string `json:"device_secret,omitempty"`
	ServerURL    string `json:"server_url,omitempty"`
	// Operator is the console user's name, shown to the person at the endpoint
	// in the session banner. Empty from an older server, which the banner reads
	// as "an administrator".
	Operator string `json:"operator,omitempty"`
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

	// The optional features. All omitempty, so a console that predates them
	// sees the same hello it always did, and a newer console shows each control
	// only when the endpoint has advertised it.
	Monitors     []MonitorInfo `json:"monitors,omitempty"`
	Clipboard    bool          `json:"clipboard,omitempty"`
	FileTransfer bool          `json:"file_transfer,omitempty"`
	MaxFileBytes int64         `json:"max_file_bytes,omitempty"`
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
	// Location is KeyboardEvent.location: 1 left, 2 right, 3 numpad. keyCode
	// alone cannot tell right Ctrl from left Ctrl or numpad Enter from Enter;
	// absent from an older console, which keeps the old behaviour.
	Location int `json:"location,omitempty"`

	// Feature messages: monitor selection, key combinations, clipboard text
	// and file upload. See handleFeatureMessage.
	Index int    `json:"index,omitempty"`
	Combo string `json:"combo,omitempty"`
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Seq   int    `json:"seq,omitempty"`
	Data  string `json:"data,omitempty"`
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

// Adaptive capture intervals: 33ms (~30 FPS) while user interacts or desktop is active,
// throttling smoothly up to 125ms (~8 FPS) when desktop is completely static.
const (
	minCaptureInterval = 33 * time.Millisecond
	maxCaptureInterval = 125 * time.Millisecond
	captureInterval    = minCaptureInterval
)

type Session struct {
	config           SessionConfig
	serverURL        string
	creds            Credentials
	capturer         ScreenCapturer
	conn             *websocket.Conn
	mu               sync.Mutex
	stopChan         chan struct{}
	stopOnce         sync.Once
	modeMu           sync.RWMutex
	mode             string
	wakeChan         chan struct{}
	refreshRequested atomic.Bool
	// inputErrMu guards lastInputErr, the time of the last input_failed
	// notice, so a run of refused input is reported once every few seconds.
	inputErrMu   sync.Mutex
	lastInputErr time.Time

	files        *fileReceiver
	progressMu   sync.Mutex
	progressSent map[string]int64

	// ownClipboardSeq is written by the input goroutine when the operator sets
	// the clipboard and read by the stream loop; the rest of the endpoint state
	// below belongs to the stream loop alone.
	ownClipboardSeq   atomic.Uint32
	ownClipboardText  atomic.Value
	lastClipboardSeq  uint32
	lastCursorX       int
	lastCursorY       int
	lastCursorVisible bool
	quality           *qualityController
	lastStats         time.Time

	banner sessionBanner
}

func NewSession(cfg SessionConfig, serverBaseURL string, creds Credentials, capturer ScreenCapturer) *Session {
	mode := cfg.Mode
	if mode == "" {
		mode = "full_control"
	}
	return &Session{
		config:    SessionConfig{SessionID: cfg.SessionID, Mode: mode, RelayURL: cfg.RelayURL, DeviceID: cfg.DeviceID, DeviceSecret: cfg.DeviceSecret, ServerURL: cfg.ServerURL, Operator: cfg.Operator},
		serverURL: serverBaseURL,
		creds:     creds,
		capturer:  capturer,
		stopChan:  make(chan struct{}),
		wakeChan:  make(chan struct{}, 1),
		mode:      mode,

		files:        newFileReceiver(defaultTransfersDir()),
		progressSent: make(map[string]int64),
		quality:      newQualityController(time.Now()),
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

	dialer := transport.NewWebSocketDialer(15 * time.Second)
	conn, _, err := dialer.DialContext(ctx, wsURL, http.Header{
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
		"capabilities": s.capabilities(),
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

	// Told before the first frame, not after: the person at the endpoint should
	// know their screen is shared from the moment it is.
	s.banner = showSessionBanner(bannerText(s.config.Operator, s.mode))

	go s.readInputLoop()
	go s.streamFramesLoop()

	return nil
}

func (s *Session) Stop() {
	s.stopOnce.Do(func() {
		// Lift held keys before the socket goes away. A session that ends while
		// the operator is holding Ctrl must not leave it held on the endpoint.
		_ = s.capturer.ReleaseAllKeys()
		s.files.closeAll()
		if s.banner != nil {
			s.banner.Close()
		}

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
	if s.banner != nil {
		s.banner.SetText(bannerText(s.config.Operator, m))
	}
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

func (s *Session) wakeCapture() {
	select {
	case s.wakeChan <- struct{}{}:
	default:
	}
}

// inputFailureEvery throttles input_failed notices: a blocked injection fails
// on every mouse move, and one message every few seconds says it.
const inputFailureEvery = 5 * time.Second

// reportInputError tells the console that input did not reach the endpoint.
// Every injection error used to be discarded, so an operator whose clicks were
// refused saw a live desktop that simply did not respond, and nothing anywhere
// said why.
func (s *Session) reportInputError(err error) {
	if err == nil {
		return
	}
	now := time.Now()
	s.inputErrMu.Lock()
	due := now.Sub(s.lastInputErr) >= inputFailureEvery
	if due {
		s.lastInputErr = now
	}
	s.inputErrMu.Unlock()
	if !due {
		return
	}
	log.Warn().Err(err).Str("session", s.config.SessionID).Msg("remote control input was not applied")
	s.SendNotice("input_failed", err.Error())
}

// normalizeKey turns a generic modifier into its left or right virtual key
// from the browser's location, so right Ctrl or right Alt (AltGr) arrive as
// themselves rather than as the left key.
func normalizeKey(in *InputEvent) {
	sides := map[int][2]int{
		0x10: {0xA0, 0xA1}, // Shift
		0x11: {0xA2, 0xA3}, // Ctrl
		0x12: {0xA4, 0xA5}, // Alt
	}
	if pair, ok := sides[in.Code]; ok {
		switch in.Location {
		case 1:
			in.Code = pair[0]
		case 2:
			in.Code = pair[1]
		}
	}
}

func (s *Session) readInputLoop() {
	// Injection is pinned to one OS thread. On Windows the thread's desktop is
	// what SendInput targets, and it is moved to follow the input desktop
	// (lock screen, UAC prompt); a goroutine that hops threads between events
	// would inject from whichever thread it last landed on.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
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
		if in.Type == "refresh" {
			s.refreshRequested.Store(true)
			s.wakeCapture()
			continue
		}

		if s.handleFeatureMessage(in) {
			continue
		}

		if s.currentMode() == "view_only" {
			continue // drop input in view_only mode
		}

		switch in.Type {
		case "mouse":
			s.reportInputError(s.capturer.InjectMouseEvent(in))
			s.wakeCapture()
		case "keyboard":
			// A 'release' action carries no key: it means "lift everything you
			// think is held", which is what the console sends on blur.
			if in.Action == "release" {
				s.reportInputError(s.capturer.ReleaseAllKeys())
			} else {
				normalizeKey(&in)
				s.reportInputError(s.capturer.InjectKeyboardEvent(in))
			}
			s.wakeCapture()
		}
	}
}

func (s *Session) streamFramesLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer s.Stop()
	// A capturer that caches a frame surface between ticks frees it here, when
	// the only loop that blits into it is done with it.
	defer func() {
		if r, ok := s.capturer.(interface{ releaseCaptureResources() }); ok {
			r.releaseCaptureResources()
		}
	}()

	interval := minCaptureInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()

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
	// activeFrames keeps capture at minCaptureInterval (30 FPS) during active bursts
	activeFrames := 0

	for {
		select {
		case <-s.stopChan:
			return
		case <-s.wakeChan:
			activeFrames = 30 // hold ~30 FPS for active interaction burst
			interval = minCaptureInterval
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(0)
			continue
		case <-timer.C:
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
				timer.Reset(interval)
				continue
			}
			// Coming back from a reported failure (a UAC prompt or the lock
			// screen went away) the desktop may look exactly as it did before,
			// so the damage diff would send nothing and the console would keep
			// its "no signal" overlay. A full frame tells it the desktop is back.
			if captureFailures >= 3 {
				damage.ForceKeyframe()
			}
			captureFailures = 0
			s.pollEndpointState()
			s.maybeSendStats(time.Now())
			if len(pixels) == 0 {
				timer.Reset(interval)
				continue
			}

			if s.refreshRequested.Swap(false) {
				damage.ForceKeyframe()
			}
			damage.SetSize(width, height)
			rects := damage.Diff(pixels)
			if len(rects) == 0 {
				// Nothing on the endpoint moved.
				idleTicks++
				if activeFrames > 0 {
					activeFrames--
				} else {
					// ponytail: step up linearly. Add PID rate controller if network RTT feedback exists.
					interval = min(interval+15*time.Millisecond, maxCaptureInterval)
				}
				timer.Reset(interval)
				continue
			}

			// Screen changed: maintain interactive framerate
			activeFrames = 15
			interval = minCaptureInterval

			if idleTicks > 0 {
				log.Debug().Int("idle_ticks", idleTicks).Msg("remote control desktop was static")
				idleTicks = 0
			}

			payload, err := EncodeFrameQuality(pixels, width, height, rects, s.quality.quality)
			if err != nil {
				log.Error().Err(err).Msg("encode remote control frame")
				timer.Reset(interval)
				continue
			}

			// The write is timed with the lock held, because that is how long
			// the socket refused the bytes: it is the link telling the agent to
			// send less, and the quality controller listens to nothing else.
			s.mu.Lock()
			writeStart := time.Now()
			err = s.conn.WriteMessage(websocket.BinaryMessage, payload)
			took := time.Since(writeStart)
			s.mu.Unlock()
			if err != nil {
				return
			}
			if s.quality.observe(len(payload), took, time.Now()) {
				log.Debug().Int("quality", s.quality.quality).Dur("write", took).Msg("remote control quality adjusted")
			}

			// Only now is the baseline safe to advance. Doing it before the
			// write marks the region as delivered on a frame that never left
			// the agent, and the console then shows the old image there until
			// the session ends, with nothing in any log to explain it.
			damage.Acknowledge(rects)
			timer.Reset(interval)
		}
	}
}

package remotecontrol

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"
)

// MonitorInfo is one display the endpoint can stream, in virtual-desktop
// coordinates. Index is what the console sends back to select it; -1 selects
// every monitor at once.
type MonitorInfo struct {
	Index   int  `json:"index"`
	X       int  `json:"x"`
	Y       int  `json:"y"`
	Width   int  `json:"width"`
	Height  int  `json:"height"`
	Primary bool `json:"primary"`
}

// The interfaces below are optional. A capturer that implements one gets the
// feature, and one that does not -- every stub platform -- keeps compiling and
// simply does not advertise it in the hello. Adding them to ScreenCapturer would
// have meant a stub method on every platform that does nothing but lie.
type monitorSelector interface {
	Monitors() []MonitorInfo
	SelectMonitor(index int) (MonitorInfo, error)
}

type cursorReporter interface {
	CursorPosition() (x, y int, visible bool)
}

type clipboardAccess interface {
	ClipboardSequence() uint32
	ClipboardText() (string, error)
	SetClipboardText(text string) (uint32, error)
}

// maxClipboardBytes caps clipboard text in either direction. A clipboard that
// holds a 40MB log file is not something an operator meant to send over a
// remote session, and it would stall every frame queued behind it.
const maxClipboardBytes = 64 << 10

// truncateUTF8 cuts text to at most n bytes without splitting a character.
func truncateUTF8(text string, n int) string {
	if len(text) <= n {
		return text
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// Virtual-key codes for the combinations a browser will not let the console
// capture: the OS takes Win, Alt+Tab and Ctrl+Shift+Esc before the page sees
// them, so the only way to send them is as a named combination.
const (
	vkTab      = 0x09
	vkShift    = 0x10
	vkControl  = 0x11
	vkMenu     = 0x12 // Alt
	vkEscape   = 0x1B
	vkSnapshot = 0x2C // Print Screen
	vkD        = 0x44
	vkE        = 0x45
	vkR        = 0x52
	vkLWin     = 0x5B
)

var keyCombos = map[string][]int{
	"win":            {vkLWin},
	"alt_tab":        {vkMenu, vkTab},
	"ctrl_shift_esc": {vkControl, vkShift, vkEscape},
	"print_screen":   {vkSnapshot},
	"win_r":          {vkLWin, vkR},
	"win_e":          {vkLWin, vkE},
	"win_d":          {vkLWin, vkD},
}

// comboEvents expands a named combination into presses in order and releases in
// reverse, which is how a person types one. Releasing in press order would
// deliver "Tab up" after "Alt up" and turn Alt+Tab into a bare Tab.
func comboEvents(combo string) ([]InputEvent, error) {
	keys, ok := keyCombos[combo]
	if !ok {
		return nil, fmt.Errorf("unknown key combination %q", combo)
	}
	events := make([]InputEvent, 0, len(keys)*2)
	for _, k := range keys {
		events = append(events, InputEvent{Type: "keyboard", Action: "down", Code: k})
	}
	for i := len(keys) - 1; i >= 0; i-- {
		events = append(events, InputEvent{Type: "keyboard", Action: "up", Code: keys[i]})
	}
	return events, nil
}

// capabilities is the hello's capability block: what the capturer says it can
// do, plus the optional features it implements. A host that cannot capture
// advertises none of them, because none of them is useful without a desktop.
func (s *Session) capabilities() Capabilities {
	caps := s.capturer.Capabilities()
	if !caps.Capture {
		return caps
	}
	if m, ok := s.capturer.(monitorSelector); ok {
		caps.Monitors = m.Monitors()
	}
	if _, ok := s.capturer.(clipboardAccess); ok {
		caps.Clipboard = true
	}
	if s.files != nil {
		caps.FileTransfer = true
		caps.MaxFileBytes = maxFileBytes
	}
	return caps
}

// handleFeatureMessage handles the console messages that are neither raw mouse
// nor raw keyboard input. It reports whether it consumed the message.
//
// Monitor selection is honoured in view_only, like refresh: choosing what to
// look at is not input. Everything else here changes the endpoint -- its
// clipboard, its files, its keyboard -- and is dropped in view_only exactly as
// mouse and keyboard are; the relay drops it too, so this is the second gate.
func (s *Session) handleFeatureMessage(in InputEvent) bool {
	switch in.Type {
	case "monitor":
		s.selectMonitor(in.Index)
		return true
	case "keys", "clipboard", "file_begin", "file_chunk", "file_end", "file_abort":
	default:
		return false
	}

	if s.currentMode() == "view_only" {
		return true
	}

	switch in.Type {
	case "keys":
		events, err := comboEvents(in.Combo)
		if err != nil {
			log.Debug().Err(err).Msg("remote control key combination")
			return true
		}
		for _, e := range events {
			s.reportInputError(s.capturer.InjectKeyboardEvent(e))
		}
		s.wakeCapture()
	case "clipboard":
		cb, ok := s.capturer.(clipboardAccess)
		if !ok {
			return true
		}
		text := truncateUTF8(in.Text, maxClipboardBytes)
		// Recorded before the write so the stream loop, which may poll between
		// the write and the store, already knows this text is the operator's.
		s.ownClipboardText.Store(text)
		seq, err := cb.SetClipboardText(text)
		if err != nil {
			s.SendNotice("clipboard_failed", err.Error())
			return true
		}
		s.ownClipboardSeq.Store(seq)
	case "file_begin":
		if err := s.files.begin(in.ID, in.Name, in.Size); err != nil {
			s.sendFileStatus("file_failed", in.ID, "reason", err.Error())
		}
	case "file_chunk":
		received, err := s.files.chunk(in.ID, in.Seq, in.Data)
		if err != nil {
			s.sendFileStatus("file_failed", in.ID, "reason", err.Error())
			return true
		}
		s.sendFileProgress(in.ID, received)
	case "file_end":
		path, err := s.files.end(in.ID)
		if err != nil {
			s.sendFileStatus("file_failed", in.ID, "reason", err.Error())
			return true
		}
		log.Info().Str("session", s.config.SessionID).Str("path", path).Msg("remote control file received")
		s.sendFileStatus("file_saved", in.ID, "path", path)
	case "file_abort":
		s.files.abort(in.ID)
	}
	return true
}

func (s *Session) sendFileStatus(kind, id, key, value string) {
	_ = s.sendControl(map[string]any{"type": kind, "id": id, key: value})
}

// sendFileProgress reports at most every 1MB or so; a progress message per
// 192KB chunk would be most of the control traffic for a large file.
func (s *Session) sendFileProgress(id string, received int64) {
	s.progressMu.Lock()
	last := s.progressSent[id]
	if received-last < 1<<20 {
		s.progressMu.Unlock()
		return
	}
	s.progressSent[id] = received
	s.progressMu.Unlock()
	_ = s.sendControl(map[string]any{"type": "file_progress", "id": id, "received": received})
}

func (s *Session) selectMonitor(index int) {
	m, ok := s.capturer.(monitorSelector)
	if !ok {
		return
	}
	chosen, err := m.SelectMonitor(index)
	if err != nil {
		s.SendNotice("monitor_failed", err.Error())
		return
	}
	// The console's canvas is resized by the next frame's header, and the
	// whole of it has to be painted: nothing from the old monitor lines up.
	s.refreshRequested.Store(true)
	s.wakeCapture()
	_ = s.sendControl(map[string]any{
		"type":   "monitor_changed",
		"index":  index,
		"width":  chosen.Width,
		"height": chosen.Height,
	})
}

// pollEndpointState runs on the capture tick and reports what changed on the
// endpoint that the frame itself does not carry: where the pointer is, and new
// clipboard text. Both are sent only on change, so an idle desktop costs nothing.
func (s *Session) pollEndpointState() {
	if cr, ok := s.capturer.(cursorReporter); ok {
		x, y, visible := cr.CursorPosition()
		// The pointer is drawn over the frame, so it is reported in frame
		// pixels, which differ from desktop pixels while the stream is scaled.
		l := s.scale.level()
		x, y = toFrame(x, l), toFrame(y, l)
		if x != s.lastCursorX || y != s.lastCursorY || visible != s.lastCursorVisible {
			s.lastCursorX, s.lastCursorY, s.lastCursorVisible = x, y, visible
			_ = s.sendControl(map[string]any{"type": "cursor", "x": x, "y": y, "visible": visible})
		}
	}

	cb, ok := s.capturer.(clipboardAccess)
	if !ok {
		return
	}
	seq := cb.ClipboardSequence()
	if seq == s.lastClipboardSeq {
		return
	}
	first := s.lastClipboardSeq == 0
	s.lastClipboardSeq = seq
	// The clipboard as it was when the session began is the endpoint user's,
	// not something they chose to share; only changes made during the session
	// are sent. A change this session made itself is the operator's own text.
	if first || seq == s.ownClipboardSeq.Load() || s.currentMode() == "view_only" {
		return
	}
	text, err := cb.ClipboardText()
	if err != nil || text == "" {
		return
	}
	// The sequence number alone does not identify our own write: clipboard
	// history and cloud sync bump it again after we close the clipboard. The
	// text the operator sent is the reliable test.
	if own, _ := s.ownClipboardText.Load().(string); own != "" && own == truncateUTF8(text, maxClipboardBytes) {
		return
	}
	_ = s.sendControl(map[string]any{"type": "clipboard", "text": truncateUTF8(text, maxClipboardBytes)})
}

// statsInterval is how often the console is told the current quality and rate.
const statsInterval = time.Second

func (s *Session) maybeSendStats(now time.Time) {
	if now.Sub(s.lastStats) < statsInterval {
		return
	}
	s.lastStats = now
	l := s.scale.level()
	_ = s.sendControl(map[string]any{
		"type":    "stats",
		"quality": s.quality.quality,
		"kbps":    s.quality.kbps,
		// scale is the frame size as a percentage of the desktop, so the
		// operator can tell a softer picture from a slow one.
		"scale": 100 * l.num / l.den,
	})
}

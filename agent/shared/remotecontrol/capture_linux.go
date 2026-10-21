//go:build linux

package remotecontrol

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/remotecontrol/x11"
)

// LinuxCapturer captures an X11 desktop and injects input through XTEST.
//
// The agent runs as root under systemd with no DISPLAY of its own, so the
// display is found the way the user's own programs see it: a /proc scan for a
// process with DISPLAY and XAUTHORITY, preferring the logged-in user's X11
// session over a login screen. The connection is made when a session starts
// (Capabilities) and again whenever it breaks, because the user may log in,
// out, or switch sessions while the agent keeps running.
//
// Wayland sessions are reported, not captured: their X clients talk to
// Xwayland, which sees none of the desktop, so a capture would be a black
// frame that looks like a working session.
type LinuxCapturer struct {
	mu       sync.Mutex
	conn     *x11.Conn
	keymap   *x11.Keymap
	reason   string
	held     map[byte]bool
	buttons  map[byte]bool
	selected int
	// The captured area in root coordinates.
	originX, originY, areaW, areaH int
}

const (
	monitorPrimary = -2
	monitorAll     = -1
)

func NewPlatformCapturer() ScreenCapturer {
	return &LinuxCapturer{held: map[byte]bool{}, buttons: map[byte]bool{}, selected: monitorPrimary}
}

// procRoot is a variable so tests can point the scan at a fake tree.
var procRoot = "/proc"

// connectX finds the user's X display and opens it.
func connectX() (*x11.Conn, string, error) {
	s := x11.FindSessions(procRoot)
	host, _ := os.Hostname()
	var lastErr error
	sawXwayland := false
	for _, c := range s.Candidates {
		num, _, err := x11.ParseDisplay(c.Display)
		if err != nil {
			continue
		}
		auths := []string{c.Xauthority}
		auths = append(auths, s.AuthFiles...)
		for _, path := range auths {
			auth, err := x11.ReadXauthority(path, num, host)
			if err != nil {
				lastErr = err
				continue
			}
			conn, err := x11.Dial(c.Display, auth)
			if err != nil {
				lastErr = err
				continue
			}
			if xw, _, _ := conn.QueryExtension("XWAYLAND"); xw {
				_ = conn.Close()
				sawXwayland = true
				break
			}
			return conn, c.Display, nil
		}
	}
	switch {
	case s.Wayland || sawXwayland:
		return nil, "", errors.New("the desktop is a Wayland session, which remote control cannot capture yet. " +
			"Log in with an X11 session (for example \"Ubuntu on Xorg\" on the login screen) to use it")
	case len(s.Candidates) == 0:
		return nil, "", errors.New("no graphical X11 session is running on this machine (nobody is logged in to a desktop, " +
			"or it is a server without one). Use the terminal instead")
	case lastErr != nil:
		return nil, "", fmt.Errorf("could not open the X11 display: %w", lastErr)
	}
	return nil, "", errors.New("could not open the X11 display")
}

// ensure returns a live connection, connecting if needed. Caller holds mu.
func (c *LinuxCapturer) ensure() (*x11.Conn, error) {
	if c.conn != nil {
		return c.conn, nil
	}
	conn, display, err := connectX()
	if err != nil {
		c.reason = err.Error()
		return nil, err
	}
	if err := conn.XTestInit(); err != nil {
		_ = conn.Close()
		c.reason = err.Error()
		return nil, err
	}
	conn.OnMappingNotify = func() { c.keymap = nil }
	c.conn, c.keymap, c.reason = conn, nil, ""
	c.applySelection()
	log.Info().Str("display", display).Int("width", conn.Screen.Width).Int("height", conn.Screen.Height).
		Msg("remote control attached to X11 display")
	return conn, nil
}

// drop forgets a broken connection so the next call reconnects.
func (c *LinuxCapturer) drop(err error) {
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.held = map[byte]bool{}
	c.buttons = map[byte]bool{}
	log.Warn().Err(err).Msg("X11 connection lost")
}

func (c *LinuxCapturer) Capabilities() Capabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.ensure(); err != nil {
		return Capabilities{Reason: err.Error()}
	}
	return Capabilities{Capture: true, Mouse: true, Keyboard: true}
}

func (c *LinuxCapturer) CaptureScreen() ([]byte, int, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, err := c.ensure()
	if err != nil {
		return nil, 0, 0, err
	}
	x, y, w, h := c.originX, c.originY, c.areaW, c.areaH
	img, err := conn.GetImage(x, y, w, h)
	if err != nil {
		var xerr *x11.Error
		if errors.As(err, &xerr) {
			// A BadMatch here is a monitor layout that changed under us (a
			// screen unplugged); re-read the geometry rather than reconnect.
			c.applySelection()
			return nil, 0, 0, err
		}
		c.drop(err)
		return nil, 0, 0, err
	}
	return img, w, h, nil
}

// applySelection recomputes the captured area. Caller holds mu.
func (c *LinuxCapturer) applySelection() {
	if c.conn == nil {
		return
	}
	scr := c.conn.Screen
	c.originX, c.originY, c.areaW, c.areaH = 0, 0, scr.Width, scr.Height
	if c.selected == monitorAll {
		return
	}
	mons, err := c.conn.Monitors()
	if err != nil || len(mons) == 0 {
		return
	}
	pick := -1
	if c.selected == monitorPrimary {
		for i, m := range mons {
			if m.Primary {
				pick = i
			}
		}
		if pick < 0 {
			pick = 0
		}
	} else if c.selected < len(mons) {
		pick = c.selected
	}
	if pick < 0 {
		return
	}
	m := clampToScreen(mons[pick], scr.Width, scr.Height)
	c.originX, c.originY, c.areaW, c.areaH = m.X, m.Y, m.Width, m.Height
}

func clampToScreen(m x11.Monitor, w, h int) x11.Monitor {
	if m.X < 0 {
		m.Width += m.X
		m.X = 0
	}
	if m.Y < 0 {
		m.Height += m.Y
		m.Y = 0
	}
	if m.X+m.Width > w {
		m.Width = w - m.X
	}
	if m.Y+m.Height > h {
		m.Height = h - m.Y
	}
	return m
}

// Monitors implements monitorSelector.
func (c *LinuxCapturer) Monitors() []MonitorInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, err := c.ensure()
	if err != nil {
		return nil
	}
	mons, err := conn.Monitors()
	if err != nil || len(mons) < 2 {
		// One monitor (or no RandR) is nothing to choose between.
		return nil
	}
	out := make([]MonitorInfo, len(mons))
	for i, m := range mons {
		out[i] = MonitorInfo{Index: i, X: m.X, Y: m.Y, Width: m.Width, Height: m.Height, Primary: m.Primary}
	}
	return out
}

// SelectMonitor implements monitorSelector.
func (c *LinuxCapturer) SelectMonitor(index int) (MonitorInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, err := c.ensure()
	if err != nil {
		return MonitorInfo{}, err
	}
	if index != monitorAll {
		mons, err := conn.Monitors()
		if err != nil || index < 0 || index >= len(mons) {
			return MonitorInfo{}, fmt.Errorf("monitor %d does not exist", index)
		}
	}
	c.selected = index
	c.applySelection()
	return MonitorInfo{Index: index, X: c.originX, Y: c.originY, Width: c.areaW, Height: c.areaH}, nil
}

// CursorPosition implements cursorReporter. GetImage does not include the
// pointer, so the console draws it from this.
func (c *LinuxCapturer) CursorPosition() (int, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return 0, 0, false
	}
	x, y, same, err := c.conn.QueryPointer()
	if err != nil {
		return 0, 0, false
	}
	x, y = x-c.originX, y-c.originY
	return x, y, same && x >= 0 && y >= 0 && x < c.areaW && y < c.areaH
}

func (c *LinuxCapturer) InjectMouseEvent(e InputEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, err := c.ensure()
	if err != nil {
		return err
	}
	if e.X >= 0 && e.Y >= 0 {
		if err := conn.FakeInput(x11.MotionNotify, 0, e.X+c.originX, e.Y+c.originY); err != nil {
			c.drop(err)
			return err
		}
	}
	button := byte(1)
	switch e.Button {
	case "middle":
		button = 2
	case "right":
		button = 3
	}
	switch e.Action {
	case "move":
		return nil
	case "down":
		c.buttons[button] = true
		return conn.FakeInput(x11.ButtonPress, button, 0, 0)
	case "up":
		delete(c.buttons, button)
		return conn.FakeInput(x11.ButtonRelease, button, 0, 0)
	case "wheel":
		// Windows wheel units: 120 per notch, positive away from the user.
		// X has no wheel axis in the core protocol: buttons 4 and 5 are one
		// notch up and down each.
		b := byte(4)
		d := e.Delta
		if d < 0 {
			b, d = 5, -d
		}
		notches := d / 120
		if notches < 1 {
			notches = 1
		}
		if notches > 10 {
			notches = 10
		}
		return c.clickNotches(conn, b, notches)
	case "hwheel":
		// Buttons 6 and 7 are one notch left and right; positive is right.
		b := byte(7)
		d := e.Delta
		if d < 0 {
			b, d = 6, -d
		}
		notches := d / 120
		if notches < 1 {
			notches = 1
		}
		if notches > 10 {
			notches = 10
		}
		return c.clickNotches(conn, b, notches)
	}
	return nil
}

// clickNotches presses and releases a scroll button once per notch.
func (c *LinuxCapturer) clickNotches(conn *x11.Conn, b byte, notches int) error {
	for i := 0; i < notches; i++ {
		if err := conn.FakeInput(x11.ButtonPress, b, 0, 0); err != nil {
			return err
		}
		if err := conn.FakeInput(x11.ButtonRelease, b, 0, 0); err != nil {
			return err
		}
	}
	return nil
}

func (c *LinuxCapturer) InjectKeyboardEvent(e InputEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn, err := c.ensure()
	if err != nil {
		return err
	}
	sym := x11.VKToKeysym(e.Code)
	if sym == 0 {
		return fmt.Errorf("no X keysym for key code %d", e.Code)
	}
	if c.keymap == nil {
		per, syms, err := conn.KeyboardMapping()
		if err != nil {
			c.drop(err)
			return err
		}
		c.keymap = x11.NewKeymap(conn.MinKeycode, per, syms)
	}
	code := c.keymap.Keycode(sym)
	if code == 0 {
		return fmt.Errorf("the keyboard layout has no key for keysym 0x%x", sym)
	}
	switch e.Action {
	case "down":
		c.held[code] = true
		return conn.FakeInput(x11.KeyPress, code, 0, 0)
	case "up":
		delete(c.held, code)
		return conn.FakeInput(x11.KeyRelease, code, 0, 0)
	}
	return nil
}

func (c *LinuxCapturer) ReleaseAllKeys() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	for code := range c.held {
		_ = c.conn.FakeInput(x11.KeyRelease, code, 0, 0)
	}
	for b := range c.buttons {
		_ = c.conn.FakeInput(x11.ButtonRelease, b, 0, 0)
	}
	c.held = map[byte]bool{}
	c.buttons = map[byte]bool{}
	return nil
}

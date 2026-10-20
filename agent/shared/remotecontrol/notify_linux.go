//go:build linux

package remotecontrol

import (
	"sync"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/remotecontrol/x11"
)

// linuxBanner is a strip across the top of the X11 screen saying the screen
// is shared, like the Windows banner. It is an override-redirect window, so
// the window manager neither frames nor moves it, and it is re-raised and
// redrawn every second: without an event loop that is what keeps it visible
// over a full-screen window and repainted after being covered.
type linuxBanner struct {
	mu    sync.Mutex
	conn  *x11.Conn
	win   uint32
	gc    uint32
	text  string
	w, h  int
	stop  chan struct{}
	close sync.Once
}

const (
	bannerBG   = 0xb91c1c // red
	bannerFG   = 0xffffff
	bannerH    = 22
	charWidth  = 6 // the "fixed" core font is 6x13
	bannerFont = "fixed"
)

func showSessionBanner(text string) sessionBanner {
	conn, _, err := connectX()
	if err != nil {
		return nil
	}
	b := &linuxBanner{conn: conn, text: text, h: bannerH, stop: make(chan struct{})}
	if err := b.create(); err != nil {
		_ = conn.Close()
		return nil
	}
	go b.keepVisible()
	return b
}

func (b *linuxBanner) create() error {
	// Room for the longer of the two texts (viewed/controlled), so a mode
	// switch never runs past the edge.
	b.w = (len(b.text)+8)*charWidth + 32
	x := (b.conn.Screen.Width - b.w) / 2
	if x < 0 {
		x = 0
	}
	win, err := b.conn.CreateOverrideWindow(x, 0, b.w, b.h, bannerBG)
	if err != nil {
		return err
	}
	font, err := b.conn.OpenFont(bannerFont)
	if err != nil {
		return err
	}
	gc, err := b.conn.CreateGC(win, bannerFG, bannerBG, font)
	if err != nil {
		return err
	}
	b.win, b.gc = win, gc
	if err := b.conn.MapRaised(win); err != nil {
		return err
	}
	b.draw()
	return b.conn.Sync()
}

func (b *linuxBanner) draw() {
	_ = b.conn.DrawText(b.win, b.gc, 16, 15, b.text)
}

func (b *linuxBanner) keepVisible() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
			b.mu.Lock()
			_ = b.conn.Raise(b.win)
			b.draw()
			err := b.conn.Sync()
			b.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (b *linuxBanner) SetText(text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text = text
	// The window was sized for the old text; the background colour fills the
	// rest, and the redraw covers the shorter text's tail.
	_ = b.conn.DrawText(b.win, b.gc, 16, 15, text+"        ")
	_ = b.conn.Sync()
}

func (b *linuxBanner) Close() {
	b.close.Do(func() {
		close(b.stop)
		b.mu.Lock()
		defer b.mu.Unlock()
		_ = b.conn.DestroyWindow(b.win)
		_ = b.conn.Sync()
		_ = b.conn.Close()
	})
}

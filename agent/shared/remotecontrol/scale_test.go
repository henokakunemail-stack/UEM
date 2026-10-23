package remotecontrol

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func solid(w, h int, b, g, r byte) []byte {
	px := make([]byte, w*h*4)
	for i := 0; i < len(px); i += 4 {
		px[i], px[i+1], px[i+2], px[i+3] = b, g, r, 0xFF
	}
	return px
}

// A flat colour must come out exactly that colour at every size: the area
// weights have to sum to one, or a grey desktop turns a shade darker the
// moment the stream steps down, and the whole canvas flickers.
func TestDownscalingKeepsAFlatColourExact(t *testing.T) {
	var d downscaler
	for _, l := range scaleLevels[1:] {
		dw, dh := scaledSize(1366, 768, l)
		out := d.scale(solid(1366, 768, 30, 140, 220), 1366, 768, dw, dh)
		if len(out) != dw*dh*4 {
			t.Fatalf("%d/%d: %d bytes for %dx%d", l.num, l.den, len(out), dw, dh)
		}
		for i := 0; i < len(out); i += 4 {
			if out[i] != 30 || out[i+1] != 140 || out[i+2] != 220 {
				t.Fatalf("%d/%d: pixel %d is %v, want 30,140,220", l.num, l.den, i/4, out[i:i+3])
			}
		}
	}
}

// A one-pixel stroke is what text is made of. Sampling one source pixel per
// destination pixel drops it on alternate rows, so letters lose strokes when
// the stream steps down; averaging the area keeps a fainter stroke instead.
func TestAThinLineSurvivesDownscaling(t *testing.T) {
	const w, h = 64, 64
	px := solid(w, h, 255, 255, 255)
	for y := 0; y < h; y++ {
		i := (y*w + 33) * 4 // a black vertical line one pixel wide
		px[i], px[i+1], px[i+2] = 0, 0, 0
	}
	var d downscaler
	for _, l := range scaleLevels[1:] {
		dw, dh := scaledSize(w, h, l)
		out := d.scale(px, w, h, dw, dh)
		for y := 0; y < dh; y++ {
			darkest := byte(255)
			for x := 0; x < dw; x++ {
				darkest = min(darkest, out[(y*dw+x)*4])
			}
			if darkest > 200 {
				t.Fatalf("%d/%d: row %d has no trace of the line (darkest %d)", l.num, l.den, y, darkest)
			}
		}
	}
}

// A click on the last pixel of a scaled frame has to land on the desktop, not
// one pixel past its edge, and mapping a point there and back must not drift.
func TestFrameCoordinatesMapInsideTheDesktop(t *testing.T) {
	const w, h = 1920, 1080
	for _, l := range scaleLevels {
		dw, dh := scaledSize(w, h, l)
		if x := toDesktop(dw-1, l); x < 0 || x >= w {
			t.Errorf("%d/%d: last column %d maps to %d, outside 0..%d", l.num, l.den, dw-1, x, w-1)
		}
		if y := toDesktop(dh-1, l); y < 0 || y >= h {
			t.Errorf("%d/%d: last row %d maps to %d, outside 0..%d", l.num, l.den, dh-1, y, h-1)
		}
		for _, x := range []int{0, 1, 500, dw - 1} {
			if back := toFrame(toDesktop(x, l), l); back != x {
				t.Errorf("%d/%d: frame x %d -> desktop %d -> frame %d", l.num, l.den, x, toDesktop(x, l), back)
			}
		}
		if toDesktop(-1, l) != -1 || toFrame(-1, l) != -1 {
			t.Errorf("%d/%d: a negative coordinate (no position) was rescaled", l.num, l.den)
		}
	}
}

// The size drops only after frames have stayed slow, not on one heavy frame;
// it never drops a small desktop below a readable width; and it comes back to
// full size once the desktop is still.
func TestTheFrameSizeFollowsTheCostOfFrames(t *testing.T) {
	c := newScaleController()
	c.setDesktopWidth(1920)
	now := time.Now()

	if c.frameSent(200*time.Millisecond, false, now) || c.level != 0 {
		t.Fatal("one slow frame changed the size")
	}
	changed := false
	for i := 1; i <= 10 && !changed; i++ {
		changed = c.frameSent(200*time.Millisecond, false, now.Add(time.Duration(i)*100*time.Millisecond))
	}
	if !changed || c.level != 1 {
		t.Fatalf("a second of slow frames left the level at %d", c.level)
	}
	// Held: further slow frames inside holdAfterChange do not step again.
	if c.frameSent(200*time.Millisecond, false, now.Add(1500*time.Millisecond)) {
		t.Fatal("the size changed again straight after a change")
	}
	idleStart := now.Add(5 * time.Second)
	if c.idle(idleStart) {
		t.Fatal("the first idle tick restored full size")
	}
	if !c.idle(idleStart.Add(sharpenAfterIdle)) || c.level != 0 {
		t.Fatalf("a still desktop did not return to full size (level %d)", c.level)
	}

	small := newScaleController()
	small.setDesktopWidth(1024)
	for i := 0; i < 50; i++ {
		small.frameSent(time.Second, true, now.Add(time.Duration(i)*200*time.Millisecond))
	}
	if small.level != 0 {
		t.Fatalf("a 1024-pixel desktop was scaled to level %d, narrower than %d pixels", small.level, minScaledWidth)
	}
}

// While frames are sent scaled down, the console's coordinates are frame
// pixels. A click has to be mapped back to the desktop before it is injected,
// or it lands up and to the left of where the operator clicked.
func TestAClickOnAScaledFrameLandsWhereTheOperatorClicked(t *testing.T) {
	c := &fakeCapturer{capture: true}
	s := NewSession(SessionConfig{SessionID: "s-1", Mode: "full_control"}, "http://localhost:1", Credentials{}, c)
	relay := relayPair(t, s)
	s.scale.set(2) // frames at half size
	go s.readInputLoop()
	t.Cleanup(s.Stop)

	b, _ := json.Marshal(InputEvent{Type: "mouse", Action: "move", X: 100, Y: 50})
	if err := relay.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(c.mouseEvents) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(c.mouseEvents) == 0 {
		t.Fatal("the click never reached the capturer")
	}
	if got := c.mouseEvents[0]; got.X != 201 || got.Y != 101 {
		t.Fatalf("a click at frame (100,50) on a half-size frame was injected at (%d,%d), want (201,101)", got.X, got.Y)
	}
}

// cursorCapturer reports a fixed pointer position on the desktop.
type cursorCapturer struct{ fakeCapturer }

func (c *cursorCapturer) CursorPosition() (int, int, bool) { return 400, 300, true }

// The console draws the pointer over the frame, so a scaled stream has to
// report it in frame pixels too.
func TestThePointerIsReportedInFramePixels(t *testing.T) {
	c := &cursorCapturer{fakeCapturer{capture: true}}
	s := NewSession(SessionConfig{SessionID: "s-1", Mode: "full_control"}, "http://localhost:1", Credentials{}, c)
	relay := relayPair(t, s)
	s.scale.set(1) // frames at 3/4
	s.pollEndpointState()
	for _, m := range readControls(t, relay, 300*time.Millisecond) {
		if m["type"] == "cursor" {
			if m["x"] != float64(300) || m["y"] != float64(225) {
				t.Fatalf("pointer at desktop (400,300) reported at (%v,%v) on a 3/4 frame, want (300,225)", m["x"], m["y"])
			}
			return
		}
	}
	t.Fatal("no cursor message was sent")
}

// wideCapturer is a 1280x720 desktop.
type wideCapturer struct{ fakeCapturer }

func (c *wideCapturer) CaptureScreen() ([]byte, int, int, error) {
	return syntheticDesktop(1280, 720, 1), 1280, 720, nil
}

// A stream that has stepped down sends frames at the smaller size, which is
// the size the console sizes its canvas to.
func TestAScaledStreamSendsSmallerFrames(t *testing.T) {
	c := &wideCapturer{fakeCapturer{capture: true}}
	s := NewSession(SessionConfig{SessionID: "s-1", Mode: "full_control"}, "http://localhost:1", Credentials{}, c)
	relay := relayPair(t, s)
	s.scaleCtl.setDesktopWidth(1280)
	s.scaleCtl.level = 1
	s.scale.set(1)
	go s.streamFramesLoop()
	t.Cleanup(s.Stop)

	_ = relay.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		kind, b, err := relay.ReadMessage()
		if err != nil {
			t.Fatalf("no frame arrived: %v", err)
		}
		if kind != websocket.BinaryMessage {
			continue
		}
		_, w, h, _, err := DecodeFrameHeader(b)
		if err != nil {
			t.Fatal(err)
		}
		if w != 960 || h != 540 {
			t.Fatalf("a 1280x720 desktop at 3/4 was sent as %dx%d, want 960x540", w, h)
		}
		return
	}
}

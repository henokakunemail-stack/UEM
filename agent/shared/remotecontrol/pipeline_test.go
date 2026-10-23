package remotecontrol

import (
	"bytes"
	"image"
	"image/jpeg"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// movingCapturer returns a desktop whose content changes on every capture for
// the first `changes` captures, then holds still. It records when each capture
// started and ended, so the pacing between captures can be measured.
type movingCapturer struct {
	fakeCapturer
	w, h    int
	changes int
	cost    time.Duration

	mu     sync.Mutex
	calls  int
	starts []time.Time
	ends   []time.Time
	last   []byte
}

func (c *movingCapturer) CaptureScreen() ([]byte, int, int, error) {
	c.mu.Lock()
	c.calls++
	n := min(c.calls, c.changes)
	c.starts = append(c.starts, time.Now())
	c.mu.Unlock()

	time.Sleep(c.cost)
	px := make([]byte, c.w*c.h*4)
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w; x++ {
			i := (y*c.w + x) * 4
			px[i], px[i+1], px[i+2], px[i+3] = byte(x*255/c.w), byte(y*255/c.h), 128, 0xFF
		}
	}
	// Capture n paints a block into tile n, and each block stays painted
	// afterwards, so every capture changes one tile no other capture touches.
	// A frame that is lost leaves its block missing from the console for good,
	// rather than being covered up by the next change.
	cols, tiles := c.w/tileSize, (c.w/tileSize)*(c.h/tileSize)
	for k := max(1, n-tiles+1); k <= n; k++ {
		tx, ty := (k%tiles)%cols*tileSize, (k%tiles)/cols*tileSize
		for y := ty + 4; y < ty+tileSize-4; y++ {
			for x := tx + 4; x < tx+tileSize-4; x++ {
				i := (y*c.w + x) * 4
				px[i], px[i+1], px[i+2] = byte(k*37), byte(k*91), byte(255-k*53)
			}
		}
	}

	c.mu.Lock()
	c.ends = append(c.ends, time.Now())
	c.last = px
	c.mu.Unlock()
	return px, c.w, c.h, nil
}

// paintFrames plays the console's part: it applies every binary frame it reads
// to a canvas and returns the canvas once no frame has arrived for d. Control
// messages (stats every second, cursor moves) do not count as activity.
func paintFrames(t *testing.T, relay *websocket.Conn, d time.Duration) (*image.RGBA, int) {
	t.Helper()
	var canvas *image.RGBA
	frames := 0
	lastFrame := time.Now()
	for {
		_ = relay.SetReadDeadline(lastFrame.Add(d))
		kind, b, err := relay.ReadMessage()
		if err != nil {
			return canvas, frames
		}
		if kind != websocket.BinaryMessage {
			continue
		}
		lastFrame = time.Now()
		w, h, rects, jpegs, err := DecodeFrame(b)
		if err != nil {
			t.Fatalf("frame %d does not decode: %v", frames, err)
		}
		if canvas == nil || canvas.Rect.Dx() != w || canvas.Rect.Dy() != h {
			canvas = image.NewRGBA(image.Rect(0, 0, w, h))
		}
		for i, r := range rects {
			img, err := jpeg.Decode(bytes.NewReader(jpegs[i]))
			if err != nil {
				t.Fatalf("rectangle %d of frame %d does not decode: %v", i, frames, err)
			}
			if img.Bounds().Dx() != r.W || img.Bounds().Dy() != r.H {
				t.Fatalf("rectangle %+v carries a %v image", r, img.Bounds())
			}
			for y := 0; y < r.H; y++ {
				for x := 0; x < r.W; x++ {
					canvas.Set(r.X+x, r.Y+y, img.At(x, y))
				}
			}
		}
		frames++
	}
}

// worstTileError is the largest average per-channel difference, over any
// tile, between the console's canvas and a BGRA desktop. JPEG is lossy, so a
// correct stream is close rather than equal everywhere; a region that was never
// re-sent still shows the band where it used to be, and that tile stands out.
func worstTileError(canvas *image.RGBA, bgra []byte, w, h int) (float64, int, int) {
	worst, wx, wy := 0.0, 0, 0
	for ty := 0; ty < h; ty += tileSize {
		for tx := 0; tx < w; tx += tileSize {
			sum, n := 0, 0
			for y := ty; y < min(ty+tileSize, h); y++ {
				for x := tx; x < min(tx+tileSize, w); x++ {
					i := (y*w + x) * 4
					for c, s := range [3]int{2, 1, 0} {
						d := int(canvas.Pix[i+c]) - int(bgra[i+s])
						if d < 0 {
							d = -d
						}
						sum += d
						n++
					}
				}
			}
			if e := float64(sum) / float64(n); e > worst {
				worst, wx, wy = e, tx, ty
			}
		}
	}
	return worst, wx, wy
}

// jpegFloor is the worst tile error of the desktop sent whole, once, at the
// quality a full-screen update gets: the error a correct stream cannot beat.
func jpegFloor(t *testing.T, bgra []byte, w, h int) float64 {
	t.Helper()
	payload, err := EncodeFrameQuality(bgra, w, h, []Rect{{W: w, H: h}}, defaultQuality)
	if err != nil {
		t.Fatal(err)
	}
	_, _, rects, jpegs, err := DecodeFrame(payload)
	if err != nil {
		t.Fatal(err)
	}
	ref := image.NewRGBA(image.Rect(0, 0, w, h))
	for i, r := range rects {
		img, err := jpeg.Decode(bytes.NewReader(jpegs[i]))
		if err != nil {
			t.Fatal(err)
		}
		for y := 0; y < r.H; y++ {
			for x := 0; x < r.W; x++ {
				ref.Set(r.X+x, r.Y+y, img.At(x, y))
			}
		}
	}
	e, _, _ := worstTileError(ref, bgra, w, h)
	return e
}

// Capture and send now overlap, and a frame's changes are acknowledged when it
// is handed to the sender rather than after it is written. Either could lose a
// region: a change acknowledged but never sent stays stale on the console for
// the rest of the session. After a burst of changes, the console must hold the
// desktop as it last was.
func TestAPipelinedStreamLeavesTheConsoleShowingTheDesktop(t *testing.T) {
	c := &movingCapturer{fakeCapturer: fakeCapturer{capture: true}, w: 320, h: 200, changes: 25}
	s := NewSession(SessionConfig{SessionID: "s-1", Mode: "full_control"}, "http://localhost:1", Credentials{}, c)
	relay := relayPair(t, s)
	go s.streamFramesLoop()
	t.Cleanup(s.Stop)

	canvas, frames := paintFrames(t, relay, 1500*time.Millisecond)
	if canvas == nil || frames < 5 {
		t.Fatalf("only %d frames arrived", frames)
	}
	c.mu.Lock()
	last := c.last
	c.mu.Unlock()
	floor := jpegFloor(t, last, c.w, c.h)
	if e, x, y := worstTileError(canvas, last, c.w, c.h); e > floor*1.5+4 {
		t.Fatalf("after %d frames the tile at (%d,%d) differs from the desktop by %.1f per channel "+
			"(JPEG alone accounts for %.1f): that part of the screen was never re-sent", frames, x, y, e, floor)
	}
}

// The next capture used to be scheduled 33ms after the previous frame had been
// captured, encoded and written, so the interval was added to all of that
// work. A capture that takes longer than the interval now starts the next one
// straight away.
func TestASlowCaptureIsNotFollowedByAFullIntervalOfIdling(t *testing.T) {
	c := &movingCapturer{fakeCapturer: fakeCapturer{capture: true}, w: 160, h: 100, changes: 1 << 30, cost: 45 * time.Millisecond}
	s := NewSession(SessionConfig{SessionID: "s-1", Mode: "full_control"}, "http://localhost:1", Credentials{}, c)
	relay := relayPair(t, s)
	go s.streamFramesLoop()
	t.Cleanup(s.Stop)
	go func() { _, _ = paintFrames(t, relay, 2*time.Second) }()

	time.Sleep(1200 * time.Millisecond)
	c.mu.Lock()
	starts, ends := append([]time.Time(nil), c.starts...), append([]time.Time(nil), c.ends...)
	c.mu.Unlock()
	if len(ends) < 6 {
		t.Fatalf("only %d captures completed", len(ends))
	}
	var gaps []time.Duration
	for i := 1; i < len(ends) && i < len(starts); i++ {
		gaps = append(gaps, starts[i].Sub(ends[i-1]))
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	if median := gaps[len(gaps)/2]; median >= 20*time.Millisecond {
		t.Fatalf("median idle time between captures is %s; with a 45ms capture and a 33ms "+
			"interval there should be none", median)
	}
}

// A large damaged area is cut into stripes so several cores encode it at once.
// The stripes must tile the area exactly -- a gap is a strip of the console
// that is never repainted, an overlap is wasted bytes -- and fall on 16-pixel
// rows so no seam shows between them.
func TestALargeRectangleIsSentAsStripesThatTileIt(t *testing.T) {
	prev := runtime.GOMAXPROCS(4)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })

	const w, h = 1920, 1080
	payload, err := EncodeFrameQuality(syntheticDesktop(w, h, 1), w, h, []Rect{{W: w, H: h}}, defaultQuality)
	if err != nil {
		t.Fatal(err)
	}
	_, _, rects, jpegs, err := DecodeFrame(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(rects) < 2 {
		t.Fatalf("a full 1080p frame was sent as %d rectangle(s); it should be split across cores", len(rects))
	}
	y := 0
	for i, r := range rects {
		if r.X != 0 || r.W != w || r.Y != y {
			t.Fatalf("stripe %d is %+v; stripes must run edge to edge and follow on from %d", i, r, y)
		}
		if i < len(rects)-1 && r.H%16 != 0 {
			t.Errorf("stripe %d is %d rows high, not a multiple of 16", i, r.H)
		}
		img, err := jpeg.Decode(bytes.NewReader(jpegs[i]))
		if err != nil || img.Bounds().Dx() != r.W || img.Bounds().Dy() != r.H {
			t.Fatalf("stripe %d does not decode to its own size: %v", i, err)
		}
		y += r.H
	}
	if y != h {
		t.Fatalf("stripes cover %d of %d rows", y, h)
	}
}

// Small damage, such as a caret or a typed character, is not split: a stripe
// of a tiny rectangle costs more in JPEG headers than it saves.
func TestSmallDamageStaysOneRectangle(t *testing.T) {
	jobs := encodeJobs([]Rect{{X: 64, Y: 32, W: 128, H: 64}}, 8, defaultQuality)
	if len(jobs) != 1 {
		t.Fatalf("a 128x64 change became %d JPEGs", len(jobs))
	}
}

package remotecontrol

import (
	"bytes"
	"encoding/binary"
	"image/jpeg"
	"testing"
)

// solidFrame builds a BGRA frame filled with one colour, which is what a still
// desktop looks like to the diff.
func solidFrame(width, height int, b, g, r, a byte) []byte {
	pixels := make([]byte, width*height*4)
	for i := 0; i < width*height; i++ {
		pixels[i*4+0] = b
		pixels[i*4+1] = g
		pixels[i*4+2] = r
		pixels[i*4+3] = a
	}
	return pixels
}

// The first frame has to be the whole desktop. A partial first frame is a
// desktop with holes in it, which is exactly what the operator sees when the
// console connects mid-session and the agent assumes it already painted one.
func TestTheFirstFrameIsTheWholeDesktop(t *testing.T) {
	d := NewDamage()
	d.SetSize(640, 480)

	rects := d.Diff(solidFrame(640, 480, 0x20, 0x30, 0x40, 0xFF))
	if len(rects) != 1 {
		t.Fatalf("first frame produced %d rectangles, want the whole screen in one", len(rects))
	}
	if r := rects[0]; r.X != 0 || r.Y != 0 || r.W != 640 || r.H != 480 {
		t.Fatalf("first frame rectangle = %+v, want the full 640x480 at the origin", r)
	}
}

// The whole point of the rewrite. An unchanged desktop must produce nothing to
// send, because before this existed every tick sent the entire 1080p screen
// whether or not anything on it had moved.
func TestAnUnchangedDesktopSendsNothing(t *testing.T) {
	d := NewDamage()
	d.SetSize(640, 480)
	frame := solidFrame(640, 480, 0x20, 0x30, 0x40, 0xFF)

	d.Diff(frame)
	for i := 0; i < 20; i++ {
		if rects := d.Diff(frame); len(rects) != 0 {
			t.Fatalf("tick %d of an unchanged desktop produced %d rectangles, want none", i, len(rects))
		}
	}
}

// A change has to be bounded to where it happened, and the payload has to be
// proportionally smaller than a full frame. Without the second half of this the
// first half would pass on a diff that reports the right rectangle while still
// sending the whole screen's worth of bytes.
func TestASmallChangeCostsFarLessThanTheWholeScreen(t *testing.T) {
	const w, h = 1280, 720
	d := NewDamage()
	d.SetSize(w, h)
	base := solidFrame(w, h, 0x20, 0x30, 0x40, 0xFF)
	d.Diff(base)

	// Move one pixel, well inside a single 32px tile.
	moved := append([]byte(nil), base...)
	moved[0] ^= 0xFF

	rects := d.Diff(moved)
	if len(rects) != 1 {
		t.Fatalf("one changed pixel produced %d rectangles, want 1", len(rects))
	}
	area := rects[0].W * rects[0].H
	if area > 32*32 {
		t.Fatalf("one changed pixel produced a %dx%d rectangle; the tile is %dpx", rects[0].W, rects[0].H, tileSize)
	}

	full, err := EncodeFrame(base, w, h, []Rect{{W: w, H: h}})
	if err != nil {
		t.Fatalf("EncodeFrame full: %v", err)
	}
	small, err := EncodeFrame(moved, w, h, rects)
	if err != nil {
		t.Fatalf("EncodeFrame small: %v", err)
	}
	if len(small) >= len(full)/4 {
		t.Fatalf("a one-pixel change cost %d bytes against %d for the whole screen; it has to be a fraction of it", len(small), len(full))
	}
	t.Logf("full frame %d bytes, one-pixel change %d bytes (%.1fx smaller)", len(full), len(small), float64(len(full))/float64(len(small)))
}

// A frame that never reaches the console must be resent. Diff used to advance
// the baseline before the caller had written anything, so an encode failure or a
// dropped socket marked the region as delivered anyway: the console kept the
// previous image there for the rest of the session, and nothing anywhere said so.
//
// The test is the shape of that failure: diff, do not acknowledge, diff again.
// The second diff has to report the same region, because the console still has
// the old pixels.
func TestARegionThatWasNeverSentIsResentOnTheNextTick(t *testing.T) {
	const w, h = 256, 256
	d := NewDamage()
	d.SetSize(w, h)
	d.Acknowledge(d.Diff(solidFrame(w, h, 0, 0, 0, 0xFF)))

	changed := solidFrame(w, h, 0, 0, 0, 0xFF)
	changed[0] ^= 0xFF

	first := d.Diff(changed)
	if len(first) == 0 {
		t.Fatal("the change was not detected")
	}

	// The caller now fails to encode or to write, so it does not acknowledge.
	second := d.Diff(changed)
	if len(second) == 0 {
		t.Fatal("the change was dropped after an unsent frame; the console would keep the old pixels for the rest of the session")
	}
	if second[0] != first[0] {
		t.Errorf("the resent rectangle moved: first %+v, then %+v", first[0], second[0])
	}

	// Once it is acknowledged, it settles: an unchanged desktop must stop
	// producing rectangles, or a static screen costs bandwidth forever.
	d.Acknowledge(second)
	if again := d.Diff(changed); len(again) != 0 {
		t.Errorf("an acknowledged region is still being resent: %+v", again)
	}
}

// An out-of-range rectangle must be dropped, not indexed. acknowledge is the
// only place that writes d.previous directly, so an unclamped rectangle is a
// panic on the streaming goroutine rather than a dropped frame.
func TestAnOutOfRangeRectangleIsDroppedNotIndexed(t *testing.T) {
	const w, h = 256, 256
	d := NewDamage()
	d.SetSize(w, h)
	d.Acknowledge(d.Diff(solidFrame(w, h, 0, 0, 0, 0xFF)))

	// None of these may panic. A partial rectangle has to be clamped, not
	// rejected, so a resolution change mid-frame degrades to a smaller update
	// that the next tick corrects.
	d.Acknowledge([]Rect{
		{X: -10, Y: 0, W: 32, H: 32},
		{X: 0, Y: -10, W: 32, H: 32},
		{X: w, Y: 0, W: 32, H: 32},
		{X: 0, Y: h, W: 32, H: 32},
		{X: w - 8, Y: h - 8, W: 64, H: 64},
		{X: 10, Y: 10, W: 0, H: 0},
		{X: 10, Y: 10, W: -5, H: -5},
	})

	if rects := d.Diff(solidFrame(w, h, 0, 0, 0, 0xFF)); len(rects) != 0 {
		t.Errorf("the clamped acknowledgements moved the baseline: %+v", rects)
	}
}

// A tile damaged in two consecutive frames has to be sent in both. The console
// only ever holds what it was last sent, so a change that is acknowledged as
// delivered before it is written leaves the operator looking at a mixture of two
// states.
func TestATileDamagedTwiceIsSentTwice(t *testing.T) {
	const w, h = 256, 256
	d := NewDamage()
	d.SetSize(w, h)
	base := solidFrame(w, h, 0, 0, 0, 0xFF)
	d.Acknowledge(d.Diff(base))

	first := append([]byte(nil), base...)
	first[0] ^= 0xFF
	if rects := d.Diff(first); len(rects) == 0 {
		t.Fatal("the first change was not detected")
	} else {
		d.Acknowledge(rects)
	}

	second := append([]byte(nil), first...)
	second[0] ^= 0xFF
	rects := d.Diff(second)
	if len(rects) == 0 {
		t.Fatal("a second change to the same tile was dropped; the console would keep the first state for ever")
	}
}

// Only the rectangles actually sent are moved into the baseline. Anything left
// out of a frame is still different on the endpoint, so the next diff has to
// find it again -- that is what makes a frame skipped for bandwidth recover
// instead of losing the change.
func TestAnUnsentRectangleResurfacesOnTheNextFrame(t *testing.T) {
	const w, h = 256, 256
	d := NewDamage()
	d.SetSize(w, h)
	base := solidFrame(w, h, 0, 0, 0, 0xFF)
	d.Diff(base)

	// Change the far corner, then acknowledge a rectangle somewhere else, as
	// a frame that could not send everything would.
	changed := append([]byte(nil), base...)
	changed[(h-1)*w*4+0] ^= 0xFF
	d.acknowledge([]Rect{{X: 0, Y: 0, W: tileSize, H: tileSize}})

	rects := d.Diff(changed)
	if len(rects) == 0 {
		t.Fatal("a change outside the acknowledged rectangle was lost; it will never be sent")
	}
	found := false
	for _, r := range rects {
		if r.Y > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("rectangles %+v do not cover the changed corner at y=%d", rects, h-1)
	}
}

// A resolution change invalidates everything: the console has just been handed
// a differently sized canvas, so nothing already on it lines up.
func TestAResolutionChangeForcesAWholeFrame(t *testing.T) {
	d := NewDamage()
	d.SetSize(640, 480)
	d.Diff(solidFrame(640, 480, 1, 2, 3, 0xFF))
	if rects := d.Diff(solidFrame(640, 480, 1, 2, 3, 0xFF)); len(rects) != 0 {
		t.Fatalf("an unchanged desktop produced %d rectangles, want none", len(rects))
	}

	d.SetSize(1280, 720)
	rects := d.Diff(solidFrame(1280, 720, 1, 2, 3, 0xFF))
	if len(rects) != 1 || rects[0].W != 1280 || rects[0].H != 720 {
		t.Fatalf("after a resolution change produced %+v, want one full 1280x720 rectangle", rects)
	}
}

// When nothing merges the frame is a mosaic of independent changes, and a long
// rectangle list costs more than one whole frame does. Collapsing is the honest
// encoding of "the whole screen changed".
func TestAMosaicOfChangesCollapsesToOneFrame(t *testing.T) {
	// Single row, so a rectangle cannot extend downward, and every second tile
	// changed, so no two dirty tiles are adjacent and nothing can merge into a
	// run. Every dirty tile therefore stays its own rectangle, which is the
	// case the collapse guard exists for.
	const w, h = tileSize * (maxRects * 4), tileSize
	d := NewDamage()
	d.SetSize(w, h)
	base := solidFrame(w, h, 0, 0, 0, 0xFF)
	d.Diff(base)

	mosaic := append([]byte(nil), base...)
	changedTiles := 0
	for tx := 0; tx < w/tileSize; tx += 2 {
		mosaic[tx*tileSize*4] ^= 0xFF
		changedTiles++
	}

	rects := d.Diff(mosaic)
	if len(rects) != 1 {
		t.Fatalf("a mosaic of %d isolated changes produced %d rectangles, want 1 full frame", changedTiles, len(rects))
	}
	if rects[0].W != w || rects[0].H != h {
		t.Fatalf("collapsed rectangle = %+v, want the whole %dx%d screen", rects[0], w, h)
	}
}

// The console reads the frame with a DataView, so the wire layout is the
// contract. Every field is checked against EncodeFrame's own writer, and the
// JPEG bodies are decoded back to prove the pixel data survives.
func TestAFrameSurvivesTheRoundTrip(t *testing.T) {
	const w, h = 96, 64
	pixels := solidFrame(w, h, 0x10, 0x20, 0x30, 0xFF)
	rects := []Rect{{X: 32, Y: 0, W: 32, H: 32}, {X: 0, Y: 32, W: 64, H: 32}}

	payload, err := EncodeFrame(pixels, w, h, rects)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}

	version, gotW, gotH, count, err := DecodeFrameHeader(payload)
	if err != nil {
		t.Fatalf("DecodeFrameHeader: %v", err)
	}
	if version != frameVersion || gotW != w || gotH != h || count != 2 {
		t.Fatalf("header = version %d, %dx%d, %d rectangles", version, gotW, gotH, count)
	}

	_, _, gotRects, bodies, err := DecodeFrame(payload)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("decoded %d bodies, want 2", len(bodies))
	}
	for i, want := range rects {
		if gotRects[i] != want {
			t.Fatalf("rectangle %d = %+v, want %+v", i, gotRects[i], want)
		}
		img, err := jpeg.Decode(bytes.NewReader(bodies[i]))
		if err != nil {
			t.Fatalf("body %d is not a decodable JPEG: %v", i, err)
		}
		if img.Bounds().Dx() != want.W || img.Bounds().Dy() != want.H {
			t.Fatalf("body %d decoded at %v, want %dx%d", i, img.Bounds(), want.W, want.H)
		}
		// BGRA in, RGBA out: the red byte of the source pixel is the red
		// channel the console paints. JPEG is lossy, so the channels are
		// compared loosely -- what is being checked is which source byte ended
		// up in which channel, not the exact value.
		gotR, gotG, gotB, _ := img.At(0, 0).RGBA()
		if absInt(int(gotR>>8)-0x30) > 8 || absInt(int(gotG>>8)-0x20) > 8 || absInt(int(gotB>>8)-0x10) > 8 {
			t.Fatalf("body %d pixel 0 decoded as R=%d G=%d B=%d, want roughly R=0x30 G=0x20 B=0x10",
				i, gotR>>8, gotG>>8, gotB>>8)
		}
	}
}

// The BGRA to RGBA swap is the one conversion in the path and getting it wrong
// swaps red and blue on the operator's screen, which reads as "the endpoint is
// broken" rather than as a colour bug.
func TestTheColourOrderIsSwappedNotJustCopied(t *testing.T) {
	pixels := make([]byte, 4)
	pixels[0] = 0x11 // blue
	pixels[1] = 0x22 // green
	pixels[2] = 0x33 // red

	encoded, err := encodeRect(pixels, 4, Rect{W: 1, H: 1})
	if err != nil {
		t.Fatalf("encodeRect: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	r, g, b, _ := img.At(0, 0).RGBA()
	// JPEG is lossy, so compare loosely: the point is which channel holds which
	// source byte, not the exact value.
	if absInt(int(r>>8)-0x33) > 8 || absInt(int(g>>8)-0x22) > 8 || absInt(int(b>>8)-0x11) > 8 {
		t.Fatalf("decoded R=%d G=%d B=%d, want roughly R=0x33 G=0x22 B=0x11", r>>8, g>>8, b>>8)
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// A rectangle that leaves the desktop would have the console read past the end
// of its canvas, so it is refused rather than clamped. A clamped rectangle would
// be a silently wrong frame.
func TestARectangleOutsideTheDesktopIsRefused(t *testing.T) {
	const w, h = 64, 64
	pixels := solidFrame(w, h, 0, 0, 0, 0xFF)

	cases := map[string]Rect{
		"past the right edge": {X: 32, Y: 0, W: 64, H: 32},
		"past the bottom":     {X: 0, Y: 32, W: 32, H: 64},
		"negative origin":     {X: -1, Y: 0, W: 32, H: 32},
		"zero width":          {X: 0, Y: 0, W: 0, H: 32},
		"zero height":         {X: 0, Y: 0, W: 32, H: 0},
	}
	for name, r := range cases {
		if _, err := EncodeFrame(pixels, w, h, []Rect{r}); err == nil {
			t.Fatalf("%s: EncodeFrame accepted %+v", name, r)
		}
	}
}

// A payload that stops mid-frame must be refused. Painting the part that did
// arrive would leave the console showing some of an update and none of the rest.
func TestATruncatedPayloadIsRefused(t *testing.T) {
	const w, h = 64, 64
	pixels := solidFrame(w, h, 0, 0, 0, 0xFF)
	payload, err := EncodeFrame(pixels, w, h, []Rect{{W: 32, H: 32}})
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}

	truncations := map[string][]byte{
		"shorter than a header": payload[:3],
		"inside the rectangle":  payload[:len(payload)-10],
		"before the body size":  payload[:6+8],
	}
	for name, truncated := range truncations {
		if _, _, _, _, err := DecodeFrame(truncated); err == nil {
			t.Fatalf("%s: DecodeFrame accepted a payload of %d bytes", name, len(truncated))
		}
	}
}

// The console refuses a version it does not recognise rather than trying to
// paint a layout it does not understand, which would be a corrupted canvas with
// no error. The agent and console are compiled separately, so this is the only
// thing that makes a renamed field fail loudly.
func TestAnUnknownLayoutVersionIsRefused(t *testing.T) {
	payload := make([]byte, 6)
	payload[0] = frameVersion + 1
	payload[1] = 0
	binary.BigEndian.PutUint16(payload[2:4], 64)
	binary.BigEndian.PutUint16(payload[4:6], 64)

	if _, _, _, _, err := DecodeFrameHeader(payload); err == nil {
		t.Fatal("DecodeFrameHeader accepted an unknown layout version")
	}
}

// The frame loop must skip the send entirely when nothing changed, so an idle
// desktop costs a blit and a diff per tick and no bandwidth at all. This is the
// assertion that pins the whole rewrite down: it is the difference between a
// live desktop and the polled screenshot stream it replaced.
func TestAnIdleDesktopCostsNoBytesAtAll(t *testing.T) {
	const w, h = 640, 480
	d := NewDamage()
	d.SetSize(w, h)
	frame := solidFrame(w, h, 0x11, 0x22, 0x33, 0xFF)

	d.Diff(frame)
	encoded := 0
	for i := 0; i < 100; i++ {
		rects := d.Diff(frame)
		if len(rects) == 0 {
			// This is what streamFramesLoop acts on: no EncodeFrame, no send.
			continue
		}
		payload, err := EncodeFrame(frame, w, h, rects)
		if err != nil {
			t.Fatalf("EncodeFrame: %v", err)
		}
		encoded += len(payload)
	}
	if encoded != 0 {
		t.Fatalf("100 ticks of an unchanged desktop produced %d bytes; the old path sent a full frame every one of them", encoded)
	}
}

package remotecontrol

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
)

// The capture loop used to encode the whole desktop into a single JPEG on every
// tick and send all of it, changed or not. At 1920x1080 that is roughly 200KB
// and fifteen milliseconds of encoding per frame whether the operator was
// typing one character or watching an idle desktop, which is what makes a
// polled capture read as a slideshow: the cost is proportional to the screen,
// not to the change.
//
// Windows has no framebuffer stream API. TightVNC and UltraViewer both do
// exactly what this file does: blit into a memory bitmap, diff it against the
// previous one, and send only the rectangles that moved. The change is the thing
// worth transmitting, so the transmission is sized to the change.

// tileSize is the edge of the unit the diff is computed on. 32px is chosen so a
// text caret or a blinking cursor damages one tile rather than being smeared
// across a large area, while a 1080p desktop still yields only 2040 tiles to
// scan per frame.
const tileSize = 32

// maxRects bounds the rectangle count in one frame. Merging already reduces a
// typical change to a handful of rectangles; this is the guard for the case
// where nothing merges, such as a full-screen video or a window being dragged
// across the desktop. Past this the frame collapses to one full-screen
// rectangle, which is the honest encoding of "the whole screen changed" and is
// still no worse than the full frame this path replaced.
const maxRects = 64

// Rect is a region of the desktop in endpoint pixel coordinates.
type Rect struct {
	X, Y, W, H int
}

// Frame version byte. The console refuses anything it does not recognise rather
// than trying to paint a layout it does not understand, which would be a
// corrupted canvas with no error.
const frameVersion = 1

// Damage tracks what changed since the last frame was sent.
//
// It holds two full frames rather than one, and that is deliberate. A tile
// damaged in frame N and again in frame N+1 has to be sent twice, because the
// console only ever holds what it was last sent: acknowledging a change while
// still holding a pre-N baseline would let frame N+1 omit the second change and
// leave the console showing a mix of two states.
type Damage struct {
	current  []byte
	previous []byte
	width    int
	height   int
	cols     int
	rows     int

	// dirty is the per-tile flag for the frame being diffed, reused across
	// frames so a 10 fps loop does not allocate a grid 10 times a second.
	dirty []bool
	// consumed marks tiles already claimed by a rectangle during the merge, so
	// the same tile is not emitted twice.
	consumed []bool

	// firstFrame is true until the console has been given the desktop once at
	// full size. Until then everything is sent, because a partial first frame
	// is a desktop with holes in it.
	firstFrame bool
}

// NewDamage returns a tracker that has never sent a frame.
func NewDamage() *Damage {
	return &Damage{firstFrame: true}
}

// SetSize rebuilds the grid when the desktop's dimensions change. Every tile is
// marked dirty: the console's canvas has just been resized and holds nothing
// that lines up with what the agent has.
func (d *Damage) SetSize(width, height int) {
	if width == d.width && height == d.height {
		return
	}
	d.width = width
	d.height = height
	d.cols = (width + tileSize - 1) / tileSize
	d.rows = (height + tileSize - 1) / tileSize
	d.current = make([]byte, width*height*4)
	d.previous = make([]byte, width*height*4)
	d.dirty = make([]bool, d.cols*d.rows)
	d.consumed = make([]bool, d.cols*d.rows)
	d.firstFrame = true
}

// Diff compares a freshly blitted frame against the baseline and returns the
// rectangles that have to be sent.
//
// pixels is BGRA, which is what GetDIBits produces and what the encoder below
// expects, so nothing is converted on the full frame: only the rectangles that
// are actually sent are swapped to RGBA for encoding.
func (d *Damage) Diff(pixels []byte) []Rect {
	if len(pixels) != len(d.current) {
		// The grid does not describe these pixels. Sending a diff computed
		// against a mismatched grid would land the update in the wrong place on
		// the console, so the only safe reading is that everything changed.
		d.firstFrame = true
	}
	copy(d.current, pixels)

	whole := Rect{W: d.width, H: d.height}
	if d.firstFrame {
		// Acknowledge the rectangle actually returned. Leaving the baseline
		// at zeros would make the very next frame diff against an empty
		// screen and resend all of it.
		d.acknowledge([]Rect{whole})
		d.firstFrame = false
		return []Rect{whole}
	}

	clear(d.dirty)
	changed := false
	for ty := 0; ty < d.rows; ty++ {
		for tx := 0; tx < d.cols; tx++ {
			if d.tileDiffers(tx, ty) {
				d.dirty[ty*d.cols+tx] = true
				changed = true
			}
		}
	}
	if !changed {
		// Nothing moved. Returning no rectangles is what lets the caller skip
		// the encode and the send entirely, so an idle desktop costs one blit
		// and one diff per tick and no bandwidth at all.
		return nil
	}

	rects := d.merge()
	// The baseline is not advanced here. Advancing it inside Diff means a
	// failure between this return and the socket write -- an EncodeFrame error,
	// a dropped connection -- leaves the region marked as already sent, so it
	// is never re-sent and the console keeps the stale image for the rest of
	// the session with nothing logged. Acknowledge is called by the caller once
	// the write has returned nil instead.
	return rects
}

// Acknowledge advances the baseline over the rectangles that have been written.
//
// Called only after the frame is on the wire. Anything that fails before that
// leaves the baseline where it was, so the next tick rediffs the same region
// and resends it, which is the only outcome an operator can recover from.
func (d *Damage) Acknowledge(rects []Rect) { d.acknowledge(rects) }

// tileDiffers reports whether any pixel in a tile changed. Pixels are compared
// eight bytes at a time because a pixel is four and the frame is a power of two
// wide in practice, so the comparison halves without a bounds check per pixel.
func (d *Damage) tileDiffers(tx, ty int) bool {
	x0 := tx * tileSize
	y0 := ty * tileSize
	x1 := min(x0+tileSize, d.width)
	y1 := min(y0+tileSize, d.height)

	rowBytes := d.width * 4
	for y := y0; y < y1; y++ {
		cur := d.current[y*rowBytes+x0*4:]
		prev := d.previous[y*rowBytes+x0*4:]
		span := (x1 - x0) * 4
		for i := 0; i+8 <= span; i += 8 {
			if binary.LittleEndian.Uint64(cur[i:]) != binary.LittleEndian.Uint64(prev[i:]) {
				return true
			}
		}
		for i := span - span%8; i < span; i++ {
			if cur[i] != prev[i] {
				return true
			}
		}
	}
	return false
}

// merge turns the per-tile flags into as few rectangles as it can.
//
// The scan is row-major greedy: take the first unconsumed dirty tile, extend it
// right across the dirty run, then extend it down for as long as that whole
// span stays dirty. It is the same approach TightVNC's region merge takes and
// it collapses a text selection, a window repaint or a scrolling list into one
// or two rectangles instead of one per tile.
func (d *Damage) merge() []Rect {
	clear(d.consumed)
	rects := make([]Rect, 0, 8)
	collapsed := false

	for ty := 0; ty < d.rows && !collapsed; ty++ {
		for tx := 0; tx < d.cols; tx++ {
			if !d.dirty[ty*d.cols+tx] || d.consumed[ty*d.cols+tx] {
				continue
			}

			// Extend right across the dirty run on this row.
			w := 1
			for tx+w < d.cols && d.dirty[ty*d.cols+tx+w] && !d.consumed[ty*d.cols+tx+w] {
				w++
			}

			// Extend down for as long as the entire span is dirty.
			h := 1
		extend:
			for ty+h < d.rows {
				for i := 0; i < w; i++ {
					if !d.dirty[(ty+h)*d.cols+tx+i] || d.consumed[(ty+h)*d.cols+tx+i] {
						break extend
					}
				}
				h++
			}

			for yy := ty; yy < ty+h; yy++ {
				for xx := tx; xx < tx+w; xx++ {
					d.consumed[yy*d.cols+xx] = true
				}
			}

			rects = append(rects, Rect{
				X: tx * tileSize,
				Y: ty * tileSize,
				W: min(w*tileSize, d.width-(tx*tileSize)),
				H: min(h*tileSize, d.height-(ty*tileSize)),
			})

			if len(rects) > maxRects {
				// Nothing is merging, so the screen genuinely is a mosaic of
				// independent changes. One full frame is both smaller and
				// simpler than a long rectangle list.
				collapsed = true
			}
		}
	}

	if collapsed {
		return []Rect{{W: d.width, H: d.height}}
	}
	return rects
}

// acknowledge moves the sent rectangles into the baseline, so the next diff is
// taken against what the console is actually holding.
//
// Only the sent rectangles are copied. Anything left out of this frame is still
// different on the endpoint, and the next diff has to find it again -- which is
// what makes a frame that was skipped for bandwidth recover on the next one
// instead of losing the change.
func (d *Damage) acknowledge(rects []Rect) {
	rowBytes := d.width * 4
	for _, r := range rects {
		// Clamp to the frame. A rectangle is derived from the tile grid, so it
		// should always be in range -- but acknowledge is the one function here
		// that indexes d.previous directly, and a resolution change between the
		// diff and the write would make an out-of-range rectangle a panic on the
		// streaming goroutine rather than a dropped frame. Clamping degrades to
		// a smaller update, which the next tick corrects.
		if r.X < 0 || r.Y < 0 || r.W <= 0 || r.H <= 0 {
			continue
		}
		x1 := min(r.X+r.W, d.width)
		y1 := min(r.Y+r.H, d.height)
		if x1 <= r.X || y1 <= r.Y {
			continue
		}
		for y := r.Y; y < y1; y++ {
			off := y*rowBytes + r.X*4
			n := (x1 - r.X) * 4
			copy(d.previous[off:off+n], d.current[off:off+n])
		}
	}
}

// EncodeFrame renders the given rectangles into the wire payload.
//
// Layout, all integers big-endian:
//
//	byte    version
//	byte    rectangle count
//	uint16  desktop width
//	uint16  desktop height
//	rect    x, y, w, h as four uint16, one per rectangle
//	rect    uint32 jpeg length, then that many bytes
//
// The dimensions travel with every frame rather than once per session because
// the console has to be able to size its canvas before the first rectangle
// arrives, and because a resolution change on the endpoint has to be able to
// reach a console that is already connected.
func EncodeFrame(pixels []byte, width, height int, rects []Rect) ([]byte, error) {
	if len(rects) > 0xFF {
		return nil, fmt.Errorf("frame carries %d rectangles, more than the header can name", len(rects))
	}
	if width > 0xFFFF || height > 0xFFFF {
		return nil, fmt.Errorf("desktop %dx%d does not fit the frame header", width, height)
	}

	// 6 bytes of header, 8 per rectangle, then the encoded pixels.
	buf := make([]byte, 0, 6+len(rects)*8+width*height/4)
	head := make([]byte, 6)
	head[0] = frameVersion
	head[1] = byte(len(rects))
	binary.BigEndian.PutUint16(head[2:4], uint16(width))
	binary.BigEndian.PutUint16(head[4:6], uint16(height))
	buf = append(buf, head...)

	rowBytes := width * 4
	for _, r := range rects {
		if r.X < 0 || r.Y < 0 || r.W <= 0 || r.H <= 0 ||
			r.X+r.W > width || r.Y+r.H > height {
			return nil, fmt.Errorf("rectangle %+v is outside the %dx%d desktop", r, width, height)
		}
		var dims [8]byte
		binary.BigEndian.PutUint16(dims[0:2], uint16(r.X))
		binary.BigEndian.PutUint16(dims[2:4], uint16(r.Y))
		binary.BigEndian.PutUint16(dims[4:6], uint16(r.W))
		binary.BigEndian.PutUint16(dims[6:8], uint16(r.H))
		buf = append(buf, dims[:]...)

		encoded, err := encodeRect(pixels, rowBytes, r)
		if err != nil {
			return nil, err
		}
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(encoded)))
		buf = append(buf, size[:]...)
		buf = append(buf, encoded...)
	}
	return buf, nil
}

// encodeRect converts one rectangle from BGRA to RGBA and JPEG-encodes it.
//
// The conversion is scoped to the rectangle. Doing it across the whole frame is
// what the previous path did, and it is a full pass over 8MB that produces
// bytes which are then thrown away whenever the diff finds the change was
// smaller than the screen.
func encodeRect(pixels []byte, rowBytes int, r Rect) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, r.W, r.H))
	for y := 0; y < r.H; y++ {
		src := pixels[(r.Y+y)*rowBytes+r.X*4:]
		dst := img.Pix[y*img.Stride : y*img.Stride+r.W*4]
		for i := 0; i < r.W; i++ {
			dst[i*4+0] = src[i*4+2] // R
			dst[i*4+1] = src[i*4+1] // G
			dst[i*4+2] = src[i*4+0] // B
			dst[i*4+3] = 0xFF
		}
	}

	var out bytes.Buffer
	// Quality 70 rather than the 60 the full-frame path used. Only the changed
	// pixels are encoded now, so the bitrate is already bounded by the size of
	// the change, and the higher quality is what keeps text on the endpoint
	// legible at the scale an operator is reading it.
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 70}); err != nil {
		return nil, fmt.Errorf("encode rect: %w", err)
	}
	return out.Bytes(), nil
}

// DecodeFrameHeader reads the fixed part of a payload. The console uses it to
// learn the desktop size and how many rectangles follow, before it has to trust
// anything else in the message.
func DecodeFrameHeader(payload []byte) (version byte, width, height, rectCount int, err error) {
	if len(payload) < 6 {
		return 0, 0, 0, 0, fmt.Errorf("frame is %d bytes, too short for a header", len(payload))
	}
	version = payload[0]
	if version != frameVersion {
		return 0, 0, 0, 0, fmt.Errorf("frame version %d is not the version this console speaks (%d)", version, frameVersion)
	}
	return version,
		int(binary.BigEndian.Uint16(payload[2:4])),
		int(binary.BigEndian.Uint16(payload[4:6])),
		int(payload[1]),
		nil
}

// DecodeFrame walks a payload back into its rectangles and their JPEG bodies.
// It is the agent's mirror of EncodeFrame and exists so the round trip is
// testable, and so a payload can be validated before anything is painted.
func DecodeFrame(payload []byte) (width, height int, rects []Rect, jpegs [][]byte, err error) {
	_, width, height, rectCount, err := DecodeFrameHeader(payload)
	if err != nil {
		return 0, 0, nil, nil, err
	}
	pos := 6
	rects = make([]Rect, rectCount)
	jpegs = make([][]byte, rectCount)
	for i := 0; i < rectCount; i++ {
		if pos+8 > len(payload) {
			return 0, 0, nil, nil, fmt.Errorf("frame ended inside rectangle %d", i)
		}
		rects[i] = Rect{
			X: int(binary.BigEndian.Uint16(payload[pos : pos+2])),
			Y: int(binary.BigEndian.Uint16(payload[pos+2 : pos+4])),
			W: int(binary.BigEndian.Uint16(payload[pos+4 : pos+6])),
			H: int(binary.BigEndian.Uint16(payload[pos+6 : pos+8])),
		}
		pos += 8
		if pos+4 > len(payload) {
			return 0, 0, nil, nil, fmt.Errorf("frame ended before the size of rectangle %d", i)
		}
		size := int(binary.BigEndian.Uint32(payload[pos : pos+4]))
		pos += 4
		if pos+size > len(payload) {
			return 0, 0, nil, nil, fmt.Errorf("frame ended inside the body of rectangle %d", i)
		}
		jpegs[i] = payload[pos : pos+size]
		pos += size
	}
	return width, height, rects, jpegs, nil
}

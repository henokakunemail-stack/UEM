package remotecontrol

import (
	"sync"
	"sync/atomic"
	"time"
)

// scaleLevel is the size frames are sent at, as a fraction of the desktop.
type scaleLevel struct{ num, den int }

// scaleLevels are the sizes the stream steps through, sharpest first. Each step
// cuts the pixels to capture-encode-send by about half: 3/4 of 1920x1080 is
// 1440x810, and 1/2 is 960x540, below which text stops being readable.
var scaleLevels = []scaleLevel{{1, 1}, {3, 4}, {1, 2}}

// minScaledWidth is the narrowest frame the stream will step down to. A small
// desktop is already cheap; scaling it further only makes it unreadable.
const minScaledWidth = 960

const (
	// slowFrame is how long encoding and sending one frame may take before the
	// stream counts as struggling: past it a scroll or a window drag runs at
	// under about 12 frames a second.
	slowFrame = 80 * time.Millisecond
	// fastFrame is a frame cheap enough that the next size up would still be
	// fluid.
	fastFrame = 25 * time.Millisecond
	// stepDownAfter is how long frames must stay slow before the size drops,
	// so one heavy frame (a window opening) does not blur the stream.
	stepDownAfter = 700 * time.Millisecond
	// stepUpFastAfter is how long frames must stay fast before one size is
	// given back while the desktop is still changing.
	stepUpFastAfter = 3 * time.Second
	// sharpenAfterIdle is how long the desktop must be still before the stream
	// returns to full size, so text the operator stops to read is sharp.
	sharpenAfterIdle = 1500 * time.Millisecond
	// holdAfterChange keeps a new size for at least this long. Every change
	// costs a full keyframe, so flapping would cost more than either size.
	holdAfterChange = 2 * time.Second
)

// scaleController picks the frame size from how long frames take to encode and
// send. It belongs to the capture loop alone; only the chosen level is shared,
// through Session.scale, with the input loop that maps coordinates back.
type scaleController struct {
	level    int
	maxLevel int
	// floor is the sharpest level allowed: 0 normally, the half-size level in
	// low-bandwidth mode.
	floor     int
	changedAt time.Time
	slowSince time.Time
	fastSince time.Time
	idleSince time.Time
}

func newScaleController() *scaleController { return &scaleController{} }

// setDesktopWidth bounds how far the size may drop for this desktop, and
// reports whether that forced the current level up (a smaller monitor was
// selected while the stream was scaled down).
func (c *scaleController) setDesktopWidth(width int) bool {
	c.maxLevel = 0
	for i := len(scaleLevels) - 1; i > 0; i-- {
		if width*scaleLevels[i].num/scaleLevels[i].den >= minScaledWidth {
			c.maxLevel = i
			break
		}
	}
	c.floor = min(c.floor, c.maxLevel)
	if c.level > c.maxLevel {
		c.level = c.maxLevel
		return true
	}
	return false
}

// setFloor sets the sharpest level allowed, and reports whether the level
// changed. Raising the floor (low-bandwidth mode on) moves the level up to it.
// Lowering it (the mode turned off) brings the level back down with it, unless
// the stream had stepped down further on its own: the operator who turns the
// mode off has asked for the normal picture, and waiting for the desktop to go
// still before giving it back reads as the switch not working.
func (c *scaleController) setFloor(floor int, now time.Time) bool {
	prev := c.floor
	c.floor = min(floor, c.maxLevel)
	switch {
	case c.level < c.floor:
		return c.set(c.floor, now)
	case c.floor < prev && c.level == prev:
		return c.set(c.floor, now)
	}
	return false
}

// frameSent records how long one frame took to encode and send, and whether the
// link was congested, and reports whether the level changed.
func (c *scaleController) frameSent(took time.Duration, congested bool, now time.Time) bool {
	c.idleSince = time.Time{}
	if now.Sub(c.changedAt) < holdAfterChange {
		c.slowSince, c.fastSince = time.Time{}, time.Time{}
		return false
	}
	switch {
	case took >= slowFrame || congested:
		c.fastSince = time.Time{}
		if c.slowSince.IsZero() {
			c.slowSince = now
		}
		if now.Sub(c.slowSince) >= stepDownAfter && c.level < c.maxLevel {
			return c.set(c.level+1, now)
		}
	case took <= fastFrame:
		c.slowSince = time.Time{}
		if c.fastSince.IsZero() {
			c.fastSince = now
		}
		if now.Sub(c.fastSince) >= stepUpFastAfter && c.level > c.floor {
			return c.set(c.level-1, now)
		}
	default:
		c.slowSince, c.fastSince = time.Time{}, time.Time{}
	}
	return false
}

// idle records a tick on which nothing changed, and reports whether the level
// went back to full size.
func (c *scaleController) idle(now time.Time) bool {
	if c.idleSince.IsZero() {
		c.idleSince = now
	}
	if c.level > c.floor && now.Sub(c.idleSince) >= sharpenAfterIdle {
		return c.set(c.floor, now)
	}
	return false
}

func (c *scaleController) set(level int, now time.Time) bool {
	if level == c.level {
		return false
	}
	c.level = level
	c.changedAt = now
	c.slowSince, c.fastSince, c.idleSince = time.Time{}, time.Time{}, time.Time{}
	return true
}

// streamScale is the level the frames on the wire are at, shared with the
// input loop. The console sends coordinates in frame pixels, so a mouse event
// is mapped back by the level of the frames the operator was looking at.
type streamScale struct{ v atomic.Int32 }

func (s *streamScale) set(level int)     { s.v.Store(int32(level)) }
func (s *streamScale) level() scaleLevel { return scaleLevels[s.v.Load()] }

// scaledSize is the frame size for a desktop at a level. Odd sizes round up so
// the last column and row of the desktop always have a pixel.
func scaledSize(width, height int, l scaleLevel) (int, int) {
	return (width*l.num + l.den - 1) / l.den, (height*l.num + l.den - 1) / l.den
}

// toDesktop maps a coordinate in frame pixels to desktop pixels: the centre of
// the frame pixel, then down. A negative coordinate means "no position" and is
// left as it is.
func toDesktop(v int, l scaleLevel) int {
	if v < 0 || l.num == l.den {
		return v
	}
	return (2*v + 1) * l.den / (2 * l.num)
}

// toFrame maps a desktop coordinate to frame pixels.
func toFrame(v int, l scaleLevel) int {
	if v < 0 || l.num == l.den {
		return v
	}
	return v * l.num / l.den
}

// tap is one source pixel's share of a destination pixel, in 1/256ths.
type tap struct {
	src    int
	weight int
}

// areaTaps lists, for each destination pixel along one axis, the source pixels
// it covers and how much of each. Averaging the covered area rather than
// sampling one pixel is what keeps thin text strokes visible when shrunk: a
// one-pixel line becomes a lighter line instead of vanishing on some rows.
func areaTaps(src, dst int) [][]tap {
	out := make([][]tap, dst)
	for d := 0; d < dst; d++ {
		// The destination pixel covers [d*src/dst, (d+1)*src/dst) in source
		// pixels, kept in units of 1/dst so the arithmetic stays exact.
		lo, hi := d*src, (d+1)*src
		var taps []tap
		total := 0
		for s := lo / dst; s*dst < hi && s < src; s++ {
			a, b := max(lo, s*dst), min(hi, (s+1)*dst)
			if b <= a {
				continue
			}
			w := (b - a) * 256 / src
			taps = append(taps, tap{src: s, weight: w})
			total += w
		}
		// Rounding leaves the weights a little short of 256; the remainder goes
		// to the largest share so a flat colour stays exactly that colour.
		if total != 256 && len(taps) > 0 {
			big := 0
			for i := range taps {
				if taps[i].weight > taps[big].weight {
					big = i
				}
			}
			taps[big].weight += 256 - total
		}
		out[d] = taps
	}
	return out
}

// downscaler shrinks BGRA frames to one size. The taps depend only on the two
// sizes, so they are built once per size rather than per frame.
type downscaler struct {
	sw, sh, dw, dh int
	xt, yt         [][]tap
}

func (d *downscaler) ensure(sw, sh, dw, dh int) {
	if d.sw == sw && d.sh == sh && d.dw == dw && d.dh == dh {
		return
	}
	d.sw, d.sh, d.dw, d.dh = sw, sh, dw, dh
	d.xt, d.yt = areaTaps(sw, dw), areaTaps(sh, dh)
}

// scale returns src (sw x sh, BGRA) shrunk to dw x dh. Rows are split across
// the cores: a full 1080p frame is about 20 million multiply-adds.
func (d *downscaler) scale(src []byte, sw, sh, dw, dh int) []byte {
	d.ensure(sw, sh, dw, dh)
	dst := make([]byte, dw*dh*4)
	workers := min(encodeWorkers(), dh)
	band := (dh + workers - 1) / workers
	var wg sync.WaitGroup
	for y0 := 0; y0 < dh; y0 += band {
		y1 := min(y0+band, dh)
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			d.rows(src, dst, y0, y1)
		}(y0, y1)
	}
	wg.Wait()
	return dst
}

func (d *downscaler) rows(src, dst []byte, y0, y1 int) {
	srcStride := d.sw * 4
	for y := y0; y < y1; y++ {
		out := dst[y*d.dw*4:]
		for x := 0; x < d.dw; x++ {
			var b, g, r int
			for _, ty := range d.yt[y] {
				row := src[ty.src*srcStride:]
				var rb, rg, rr int
				for _, tx := range d.xt[x] {
					p := row[tx.src*4:]
					rb += int(p[0]) * tx.weight
					rg += int(p[1]) * tx.weight
					rr += int(p[2]) * tx.weight
				}
				b += rb * ty.weight
				g += rg * ty.weight
				r += rr * ty.weight
			}
			o := out[x*4:]
			o[0] = byte((b + 32768) >> 16)
			o[1] = byte((g + 32768) >> 16)
			o[2] = byte((r + 32768) >> 16)
			o[3] = 0xFF
		}
	}
}

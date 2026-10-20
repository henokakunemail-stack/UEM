package x11

import (
	"errors"
	"fmt"
)

// Core request opcodes used here.
const (
	opCreateWindow      = 1
	opDestroyWindow     = 4
	opMapWindow         = 8
	opConfigureWindow   = 12
	opQueryPointer      = 38
	opQueryKeymap       = 44
	opOpenFont          = 45
	opCreateGC          = 55
	opChangeGC          = 56
	opFreeGC            = 60
	opPolyFillRectangle = 70
	opGetImage          = 73
	opImageText8        = 76
	opQueryExtension    = 98
	opGetKeyboardMap    = 101
)

// QueryExtension asks whether an extension is present and caches the answer.
func (xc *Conn) QueryExtension(name string) (present bool, major byte, err error) {
	xc.mu.Lock()
	if e, ok := xc.ext[name]; ok {
		xc.mu.Unlock()
		return e.present, e.major, nil
	}
	xc.mu.Unlock()
	body := make([]byte, 4, 4+pad4(len(name)))
	le.PutUint16(body, uint16(len(name)))
	body = append(body, padded([]byte(name))...)
	r, err := xc.roundTrip(opQueryExtension, 0, body)
	if err != nil {
		return false, 0, err
	}
	e := extInfo{present: r[8] != 0, major: r[9], firstEvent: r[10], firstError: r[11]}
	xc.mu.Lock()
	xc.ext[name] = e
	xc.mu.Unlock()
	return e.present, e.major, nil
}

// GetImage reads a rectangle of the root window and returns it as BGRA, four
// bytes per pixel, rows unpadded. The alpha byte is 0xff.
func (xc *Conn) GetImage(x, y, w, h int) ([]byte, error) {
	s := xc.Screen
	if s.BPP != 32 {
		return nil, fmt.Errorf("unsupported X pixel format: depth %d at %d bits per pixel", s.Depth, s.BPP)
	}
	body := make([]byte, 16)
	le.PutUint32(body, s.Root)
	le.PutUint16(body[4:], uint16(int16(x)))
	le.PutUint16(body[6:], uint16(int16(y)))
	le.PutUint16(body[8:], uint16(w))
	le.PutUint16(body[10:], uint16(h))
	le.PutUint32(body[12:], 0xffffffff)

	xc.mu.Lock()
	defer xc.mu.Unlock()
	seq, err := xc.send(opGetImage, 2 /* ZPixmap */, body)
	if err != nil {
		return nil, err
	}
	r, err := xc.reply(seq)
	if err != nil {
		return nil, err
	}
	src := r[32:]
	n := w * h * 4
	if len(src) < n {
		return nil, fmt.Errorf("X GetImage returned %d bytes for %dx%d", len(src), w, h)
	}
	return toBGRA(src[:n], s.Visual), nil
}

// toBGRA converts 32-bit ZPixmap pixels (little-endian words) to BGRA.
func toBGRA(src []byte, v Visual) []byte {
	out := make([]byte, len(src))
	switch {
	case v.RedMask == 0xff0000 && v.GreenMask == 0xff00 && v.BlueMask == 0xff:
		// The usual TrueColor layout: bytes are already B, G, R, X.
		copy(out, src)
		for i := 3; i < len(out); i += 4 {
			out[i] = 0xff
		}
	case v.RedMask == 0xff && v.GreenMask == 0xff00 && v.BlueMask == 0xff0000:
		for i := 0; i+3 < len(src); i += 4 {
			out[i], out[i+1], out[i+2], out[i+3] = src[i+2], src[i+1], src[i], 0xff
		}
	default:
		rs, gs, bs := shift(v.RedMask), shift(v.GreenMask), shift(v.BlueMask)
		for i := 0; i+3 < len(src); i += 4 {
			p := le.Uint32(src[i:])
			out[i] = byte((p & v.BlueMask) >> bs)
			out[i+1] = byte((p & v.GreenMask) >> gs)
			out[i+2] = byte((p & v.RedMask) >> rs)
			out[i+3] = 0xff
		}
	}
	return out
}

// shift is how far right a channel mask has to move to land in the low byte
// (keeping its top 8 bits for masks wider than a byte).
func shift(mask uint32) uint {
	if mask == 0 {
		return 0
	}
	var s uint
	for mask&1 == 0 {
		mask >>= 1
		s++
	}
	for mask > 0xff {
		mask >>= 1
		s++
	}
	return s
}

// QueryPointer returns the pointer position on the root window.
func (xc *Conn) QueryPointer() (x, y int, sameScreen bool, err error) {
	body := make([]byte, 4)
	le.PutUint32(body, xc.Screen.Root)
	r, err := xc.roundTrip(opQueryPointer, 0, body)
	if err != nil {
		return 0, 0, false, err
	}
	return int(int16(le.Uint16(r[16:]))), int(int16(le.Uint16(r[18:]))), r[1] != 0, nil
}

// QueryKeymap returns the 256-bit vector of keys currently down.
func (xc *Conn) QueryKeymap() ([32]byte, error) {
	var keys [32]byte
	r, err := xc.roundTrip(opQueryKeymap, 0, nil)
	if err != nil {
		return keys, err
	}
	copy(keys[:], r[8:40])
	return keys, nil
}

// KeyboardMapping returns keysyms per keycode, indexed from MinKeycode.
func (xc *Conn) KeyboardMapping() (perCode int, syms []uint32, err error) {
	count := int(xc.MaxKeycode) - int(xc.MinKeycode) + 1
	body := []byte{xc.MinKeycode, byte(count), 0, 0}
	r, err := xc.roundTrip(opGetKeyboardMap, 0, body)
	if err != nil {
		return 0, nil, err
	}
	perCode = int(r[1])
	n := perCode * count
	if len(r) < 32+n*4 {
		return 0, nil, errors.New("short GetKeyboardMapping reply")
	}
	syms = make([]uint32, n)
	for i := range syms {
		syms[i] = le.Uint32(r[32+i*4:])
	}
	return perCode, syms, nil
}

// XTEST fake input event types.
const (
	KeyPress      = 2
	KeyRelease    = 3
	ButtonPress   = 4
	ButtonRelease = 5
	MotionNotify  = 6
)

// XTestInit checks for XTEST and negotiates its version. Input injection is
// unavailable without it.
func (xc *Conn) XTestInit() error {
	ok, major, err := xc.QueryExtension("XTEST")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the X server has no XTEST extension, so input cannot be injected")
	}
	_, err = xc.roundTrip(major, 0, []byte{2, 0, 2, 0}) // GetVersion 2.2
	return err
}

// FakeInput injects one event through XTEST. For MotionNotify, x and y are
// absolute root coordinates and detail is 0.
func (xc *Conn) FakeInput(typ, detail byte, x, y int) error {
	_, major, err := xc.QueryExtension("XTEST")
	if err != nil {
		return err
	}
	body := make([]byte, 32)
	body[0], body[1] = typ, detail
	if typ == MotionNotify {
		le.PutUint32(body[8:], xc.Screen.Root)
		le.PutUint16(body[20:], uint16(int16(x)))
		le.PutUint16(body[22:], uint16(int16(y)))
	}
	return xc.sendVoid(major, 2, body)
}

// Monitor is one RandR monitor in root-window coordinates.
type Monitor struct {
	X, Y, Width, Height int
	Primary             bool
}

// Monitors lists the active monitors through RandR 1.5. A server without it
// returns an error; the caller then captures the whole root window.
func (xc *Conn) Monitors() ([]Monitor, error) {
	ok, major, err := xc.QueryExtension("RANDR")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("no RANDR extension")
	}
	v := make([]byte, 8)
	le.PutUint32(v, 1)
	le.PutUint32(v[4:], 5)
	r, err := xc.roundTrip(major, 0, v) // QueryVersion
	if err != nil {
		return nil, err
	}
	if maj, min := le.Uint32(r[8:]), le.Uint32(r[12:]); maj < 1 || (maj == 1 && min < 5) {
		return nil, fmt.Errorf("RANDR %d.%d has no GetMonitors", maj, min)
	}
	body := make([]byte, 8)
	le.PutUint32(body, xc.Screen.Root)
	body[4] = 1 // active monitors only
	r, err = xc.roundTrip(major, 42, body)
	if err != nil {
		return nil, err
	}
	n := int(le.Uint32(r[12:]))
	off := 32
	out := make([]Monitor, 0, n)
	for i := 0; i < n; i++ {
		if off+24 > len(r) {
			return nil, errors.New("short RRGetMonitors reply")
		}
		nOut := int(le.Uint16(r[off+6:]))
		out = append(out, Monitor{
			Primary: r[off+4] != 0,
			X:       int(int16(le.Uint16(r[off+8:]))),
			Y:       int(int16(le.Uint16(r[off+10:]))),
			Width:   int(le.Uint16(r[off+12:])),
			Height:  int(le.Uint16(r[off+14:])),
		})
		off += 24 + 4*nOut
	}
	return out, nil
}

// CreateOverrideWindow makes an unmanaged top-level window (no frame, not
// moved by the window manager), filled with background.
func (xc *Conn) CreateOverrideWindow(x, y, w, h int, background uint32) (uint32, error) {
	wid, err := xc.NewID()
	if err != nil {
		return 0, err
	}
	body := make([]byte, 28+8)
	le.PutUint32(body, wid)
	le.PutUint32(body[4:], xc.Screen.Root)
	le.PutUint16(body[8:], uint16(int16(x)))
	le.PutUint16(body[10:], uint16(int16(y)))
	le.PutUint16(body[12:], uint16(w))
	le.PutUint16(body[14:], uint16(h))
	le.PutUint16(body[16:], 0) // border
	le.PutUint16(body[18:], 1) // InputOutput
	le.PutUint32(body[20:], 0) // CopyFromParent visual
	le.PutUint32(body[24:], 0x2|0x200)
	le.PutUint32(body[28:], background) // back-pixel
	le.PutUint32(body[32:], 1)          // override-redirect
	return wid, xc.sendVoid(opCreateWindow, 0, body)
}

// MapRaised maps a window and raises it above its siblings.
func (xc *Conn) MapRaised(wid uint32) error {
	b := make([]byte, 4)
	le.PutUint32(b, wid)
	if err := xc.sendVoid(opMapWindow, 0, b); err != nil {
		return err
	}
	return xc.Raise(wid)
}

// Raise restacks a window on top.
func (xc *Conn) Raise(wid uint32) error {
	b := make([]byte, 12)
	le.PutUint32(b, wid)
	le.PutUint16(b[4:], 0x40) // stack-mode
	le.PutUint32(b[8:], 0)    // Above
	return xc.sendVoid(opConfigureWindow, 0, b)
}

// DestroyWindow destroys a window.
func (xc *Conn) DestroyWindow(wid uint32) error {
	b := make([]byte, 4)
	le.PutUint32(b, wid)
	return xc.sendVoid(opDestroyWindow, 0, b)
}

// OpenFont opens a core font by name.
func (xc *Conn) OpenFont(name string) (uint32, error) {
	fid, err := xc.NewID()
	if err != nil {
		return 0, err
	}
	body := make([]byte, 8, 8+pad4(len(name)))
	le.PutUint32(body, fid)
	le.PutUint16(body[4:], uint16(len(name)))
	body = append(body, padded([]byte(name))...)
	return fid, xc.sendVoid(opOpenFont, 0, body)
}

// CreateGC makes a graphics context with a foreground, background and
// (optionally, non-zero) font.
func (xc *Conn) CreateGC(drawable, fg, bg, font uint32) (uint32, error) {
	gc, err := xc.NewID()
	if err != nil {
		return 0, err
	}
	mask := uint32(0x4 | 0x8)
	vals := []uint32{fg, bg}
	if font != 0 {
		mask |= 0x4000
		vals = append(vals, font)
	}
	body := make([]byte, 12+4*len(vals))
	le.PutUint32(body, gc)
	le.PutUint32(body[4:], drawable)
	le.PutUint32(body[8:], mask)
	for i, v := range vals {
		le.PutUint32(body[12+4*i:], v)
	}
	return gc, xc.sendVoid(opCreateGC, 0, body)
}

// FreeGC releases a graphics context.
func (xc *Conn) FreeGC(gc uint32) error {
	b := make([]byte, 4)
	le.PutUint32(b, gc)
	return xc.sendVoid(opFreeGC, 0, b)
}

// FillRect fills a rectangle with the GC's foreground.
func (xc *Conn) FillRect(drawable, gc uint32, x, y, w, h int) error {
	b := make([]byte, 16)
	le.PutUint32(b, drawable)
	le.PutUint32(b[4:], gc)
	le.PutUint16(b[8:], uint16(int16(x)))
	le.PutUint16(b[10:], uint16(int16(y)))
	le.PutUint16(b[12:], uint16(w))
	le.PutUint16(b[14:], uint16(h))
	return xc.sendVoid(opPolyFillRectangle, 0, b)
}

// DrawText draws Latin-1 text with the GC's font; x, y is the baseline start.
func (xc *Conn) DrawText(drawable, gc uint32, x, y int, text string) error {
	if len(text) > 255 {
		text = text[:255]
	}
	b := make([]byte, 12, 12+pad4(len(text)))
	le.PutUint32(b, drawable)
	le.PutUint32(b[4:], gc)
	le.PutUint16(b[8:], uint16(int16(x)))
	le.PutUint16(b[10:], uint16(int16(y)))
	b = append(b, padded([]byte(text))...)
	return xc.sendVoid(opImageText8, byte(len(text)), b)
}

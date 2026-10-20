// Package x11 is the small part of the X11 protocol remote control needs:
// connect with an Xauthority cookie, read the screen (GetImage), read the
// pointer, inject input through XTEST, list monitors through RandR, and draw a
// notice window. It speaks the wire protocol directly so the agent stays a
// CGO-free static binary; libX11 and xcb are not linked.
//
// The protocol is little-endian here: the client picks the byte order in the
// setup request and the server answers in it.
package x11

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

var le = binary.LittleEndian

// Visual is the part of the root visual GetImage pixels are decoded with.
type Visual struct {
	ID        uint32
	RedMask   uint32
	GreenMask uint32
	BlueMask  uint32
}

// Screen describes the root window the connection captures.
type Screen struct {
	Root      uint32
	Width     int
	Height    int
	Depth     byte
	BPP       byte // bits per pixel of the root depth in ZPixmap images
	Visual    Visual
	BlackPx   uint32
	WhitePx   uint32
	Colormap  uint32
	ScreenNum int
}

// Conn is one connection to an X server. Requests are serialised: every
// method holds the connection for its request and, when there is one, its
// reply, so the capture loop and the input path can share it.
type Conn struct {
	mu     sync.Mutex
	c      net.Conn
	seq    uint16
	Screen Screen

	MinKeycode, MaxKeycode byte

	ridBase, ridMask, ridNext uint32

	ext map[string]extInfo
	buf []byte // reused for large replies (GetImage)

	// voidErr is the first error answering a request that has no reply, kept
	// until Sync reports it.
	voidErr *Error

	// OnMappingNotify is called (with the lock held) when the server announces
	// a keyboard mapping change, so a cached keymap can be dropped.
	OnMappingNotify func()
}

type extInfo struct {
	present    bool
	major      byte
	firstEvent byte
	firstError byte
}

// Error is an X protocol error answering one of our requests.
type Error struct {
	Code     byte
	BadValue uint32
	MajorOp  byte
	MinorOp  uint16
}

func (e *Error) Error() string {
	return fmt.Sprintf("X error %d (%s) on request %d.%d, value 0x%x",
		e.Code, errorName(e.Code), e.MajorOp, e.MinorOp, e.BadValue)
}

func errorName(code byte) string {
	names := []string{"", "Request", "Value", "Window", "Pixmap", "Atom", "Cursor", "Font",
		"Match", "Drawable", "Access", "Alloc", "Colormap", "GContext", "IDChoice", "Name",
		"Length", "Implementation"}
	if int(code) < len(names) {
		return names[code]
	}
	return "extension"
}

// ParseDisplay splits ":1", ":1.0" or "unix:1" into a display and screen
// number. Remote displays ("host:0") are refused: remote control reads the
// local screen only.
func ParseDisplay(d string) (display, screen int, err error) {
	host, rest, ok := strings.Cut(d, ":")
	if !ok || (host != "" && host != "unix" && host != "localhost") {
		return 0, 0, fmt.Errorf("unsupported DISPLAY %q", d)
	}
	num, scr, _ := strings.Cut(rest, ".")
	display, err = strconv.Atoi(num)
	if err != nil || display < 0 {
		return 0, 0, fmt.Errorf("bad DISPLAY %q", d)
	}
	if scr != "" {
		if screen, err = strconv.Atoi(scr); err != nil || screen < 0 {
			return 0, 0, fmt.Errorf("bad DISPLAY screen %q", d)
		}
	}
	return display, screen, nil
}

// Dial connects to a local display over its Unix socket.
func Dial(display string, auth *Auth) (*Conn, error) {
	num, screen, err := ParseDisplay(display)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/tmp/.X11-unix/X%d", num)
	c, err := net.DialTimeout("unix", path, 3*time.Second)
	if err != nil {
		// Some servers listen only on the abstract socket of the same name.
		c2, err2 := net.DialTimeout("unix", "@"+path, 3*time.Second)
		if err2 != nil {
			return nil, fmt.Errorf("connect to X display %s: %w", display, err)
		}
		c = c2
	}
	xc, err := NewConn(c, auth, screen)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return xc, nil
}

// NewConn runs the connection setup over an established stream.
func NewConn(c net.Conn, auth *Auth, screen int) (*Conn, error) {
	var name, data []byte
	if auth != nil {
		name, data = []byte(auth.Name), auth.Data
	}
	req := make([]byte, 12, 12+pad4(len(name))+pad4(len(data)))
	req[0] = 'l'
	le.PutUint16(req[2:], 11)
	le.PutUint16(req[4:], 0)
	le.PutUint16(req[6:], uint16(len(name)))
	le.PutUint16(req[8:], uint16(len(data)))
	req = append(req, padded(name)...)
	req = append(req, padded(data)...)

	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	defer c.SetDeadline(time.Time{})
	if _, err := c.Write(req); err != nil {
		return nil, err
	}
	head := make([]byte, 8)
	if _, err := io.ReadFull(c, head); err != nil {
		return nil, fmt.Errorf("x11 setup: %w", err)
	}
	more := make([]byte, int(le.Uint16(head[6:]))*4)
	if _, err := io.ReadFull(c, more); err != nil {
		return nil, fmt.Errorf("x11 setup: %w", err)
	}
	switch head[0] {
	case 1:
	case 0:
		n := int(head[1])
		if n > len(more) {
			n = len(more)
		}
		return nil, fmt.Errorf("the X server refused the connection: %s", strings.TrimSpace(string(more[:n])))
	default:
		return nil, errors.New("the X server asked for further authentication, which is not supported")
	}

	xc := &Conn{c: c, ext: map[string]extInfo{}}
	if err := xc.parseSetup(more, screen); err != nil {
		return nil, err
	}
	return xc, nil
}

func (xc *Conn) parseSetup(b []byte, want int) error {
	if len(b) < 32 {
		return errors.New("x11 setup reply too short")
	}
	xc.ridBase = le.Uint32(b[4:])
	xc.ridMask = le.Uint32(b[8:])
	vendorLen := int(le.Uint16(b[16:]))
	numScreens := int(b[20])
	numFormats := int(b[21])
	xc.MinKeycode, xc.MaxKeycode = b[26], b[27]
	off := 32 + pad4(vendorLen)

	bppFor := map[byte]byte{}
	for i := 0; i < numFormats; i++ {
		if off+8 > len(b) {
			return errors.New("x11 setup: truncated formats")
		}
		bppFor[b[off]] = b[off+1]
		off += 8
	}
	for s := 0; s < numScreens; s++ {
		if off+40 > len(b) {
			return errors.New("x11 setup: truncated screens")
		}
		scr := Screen{
			Root:      le.Uint32(b[off:]),
			Colormap:  le.Uint32(b[off+4:]),
			WhitePx:   le.Uint32(b[off+8:]),
			BlackPx:   le.Uint32(b[off+12:]),
			Width:     int(le.Uint16(b[off+20:])),
			Height:    int(le.Uint16(b[off+22:])),
			Depth:     b[off+38],
			ScreenNum: s,
		}
		scr.Visual.ID = le.Uint32(b[off+32:])
		numDepths := int(b[off+39])
		off += 40
		for d := 0; d < numDepths; d++ {
			if off+8 > len(b) {
				return errors.New("x11 setup: truncated depths")
			}
			nVis := int(le.Uint16(b[off+2:]))
			off += 8
			for v := 0; v < nVis; v++ {
				if off+24 > len(b) {
					return errors.New("x11 setup: truncated visuals")
				}
				if le.Uint32(b[off:]) == scr.Visual.ID {
					scr.Visual.RedMask = le.Uint32(b[off+8:])
					scr.Visual.GreenMask = le.Uint32(b[off+12:])
					scr.Visual.BlueMask = le.Uint32(b[off+16:])
				}
				off += 24
			}
		}
		scr.BPP = bppFor[scr.Depth]
		if s == want {
			xc.Screen = scr
			return nil
		}
	}
	return fmt.Errorf("the X display has no screen %d", want)
}

// Close closes the connection.
func (xc *Conn) Close() error { return xc.c.Close() }

// NewID allocates a resource id for a window, GC or font.
func (xc *Conn) NewID() (uint32, error) {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	if xc.ridMask == 0 {
		return 0, errors.New("the X server gave no resource ids")
	}
	step := xc.ridMask & -xc.ridMask
	xc.ridNext += step
	if xc.ridNext&^xc.ridMask != 0 {
		return 0, errors.New("out of X resource ids")
	}
	return xc.ridBase | xc.ridNext, nil
}

// send writes one request. The caller holds mu. body excludes the 4-byte
// header; it is padded and the length field filled in here.
func (xc *Conn) send(opcode, data byte, body []byte) (uint16, error) {
	n := 4 + len(body)
	total := pad4(n)
	if total/4 > 0xffff {
		return 0, errors.New("x11 request too large")
	}
	req := make([]byte, total)
	req[0], req[1] = opcode, data
	le.PutUint16(req[2:], uint16(total/4))
	copy(req[4:], body)
	_ = xc.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := xc.c.Write(req); err != nil {
		return 0, err
	}
	xc.seq++
	return xc.seq, nil
}

// reply reads packets until the reply (or error) for seq arrives. Events are
// dropped, except MappingNotify which invalidates the keymap. An error for an
// earlier request that had no reply is kept for Sync: it answered input
// injected without waiting.
//
// The returned slice is valid until the next request on the connection.
func (xc *Conn) reply(seq uint16) ([]byte, error) {
	_ = xc.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer xc.c.SetReadDeadline(time.Time{})
	head := make([]byte, 32)
	for {
		if _, err := io.ReadFull(xc.c, head); err != nil {
			return nil, err
		}
		got := le.Uint16(head[2:])
		switch head[0] {
		case 0:
			xerr := &Error{Code: head[1], BadValue: le.Uint32(head[4:]),
				MinorOp: le.Uint16(head[8:]), MajorOp: head[10]}
			if got == seq {
				return nil, xerr
			}
			if xc.voidErr == nil {
				xc.voidErr = xerr
			}
		case 1:
			extra := int(le.Uint32(head[4:])) * 4
			need := 32 + extra
			if cap(xc.buf) < need {
				xc.buf = make([]byte, need)
			}
			b := xc.buf[:need]
			copy(b, head)
			if _, err := io.ReadFull(xc.c, b[32:]); err != nil {
				return nil, err
			}
			if got == seq {
				return b, nil
			}
		case 35: // GenericEvent carries a length like a reply
			extra := int(le.Uint32(head[4:])) * 4
			if _, err := io.CopyN(io.Discard, xc.c, int64(extra)); err != nil {
				return nil, err
			}
		default:
			if head[0]&0x7f == 34 && xc.OnMappingNotify != nil { // MappingNotify
				xc.OnMappingNotify()
			}
		}
		// Sequence numbers are 16-bit; anything "after" ours means our reply
		// was lost, which the protocol does not allow. Stop rather than hang.
		if int16(got-seq) > 0 {
			return nil, fmt.Errorf("x11 reply for request %d never arrived", seq)
		}
	}
}

// roundTrip sends a request that has a reply and returns the reply.
func (xc *Conn) roundTrip(opcode, data byte, body []byte) ([]byte, error) {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	seq, err := xc.send(opcode, data, body)
	if err != nil {
		return nil, err
	}
	b, err := xc.reply(seq)
	if err != nil {
		return nil, err
	}
	// Copy out of the shared buffer for small replies so callers may keep them.
	if len(b) <= 4096 {
		out := make([]byte, len(b))
		copy(out, b)
		return out, nil
	}
	return b, nil
}

// sendVoid sends a request without a reply.
func (xc *Conn) sendVoid(opcode, data byte, body []byte) error {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	_, err := xc.send(opcode, data, body)
	return err
}

// Sync waits until the server has processed every request sent so far and
// returns the first error any request without a reply produced since the last
// Sync. It uses GetInputFocus, the conventional no-op with a reply.
func (xc *Conn) Sync() error {
	if _, err := xc.roundTrip(43, 0, nil); err != nil {
		return err
	}
	xc.mu.Lock()
	defer xc.mu.Unlock()
	if e := xc.voidErr; e != nil {
		xc.voidErr = nil
		return e
	}
	return nil
}

func pad4(n int) int { return (n + 3) &^ 3 }

func padded(b []byte) []byte {
	out := make([]byte, pad4(len(b)))
	copy(out, b)
	return out
}

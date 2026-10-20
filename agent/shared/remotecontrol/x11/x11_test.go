package x11

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseDisplay(t *testing.T) {
	for in, want := range map[string][2]int{":0": {0, 0}, ":1.0": {1, 0}, "unix:2": {2, 0}, ":3.1": {3, 1}} {
		d, s, err := ParseDisplay(in)
		if err != nil || d != want[0] || s != want[1] {
			t.Errorf("ParseDisplay(%q) = %d,%d,%v want %v", in, d, s, err, want)
		}
	}
	for _, bad := range []string{"", "0", "remote.host:0", ":x", ":-1"} {
		if _, _, err := ParseDisplay(bad); err == nil {
			t.Errorf("ParseDisplay(%q) accepted", bad)
		}
	}
}

func writeXauth(t *testing.T, entries ...xauthEntry) string {
	t.Helper()
	var b bytes.Buffer
	field := func(s []byte) {
		_ = binary.Write(&b, binary.BigEndian, uint16(len(s)))
		b.Write(s)
	}
	for _, e := range entries {
		_ = binary.Write(&b, binary.BigEndian, e.family)
		field([]byte(e.address))
		field([]byte(e.number))
		field([]byte(e.name))
		field(e.data)
	}
	p := filepath.Join(t.TempDir(), "Xauthority")
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadXauthorityPicksThisDisplayAndHost(t *testing.T) {
	p := writeXauth(t,
		xauthEntry{familyLocal, "otherhost", "0", cookieName, []byte("other")},
		xauthEntry{familyWild, "", "0", cookieName, []byte("wild")},
		xauthEntry{familyLocal, "myhost", "1", cookieName, []byte("display1")},
		xauthEntry{familyLocal, "myhost", "0", "XDM-AUTHORIZATION-1", []byte("xdm")},
		xauthEntry{familyLocal, "myhost", "0", cookieName, []byte("mine")},
	)
	a, err := ReadXauthority(p, 0, "myhost")
	if err != nil || a == nil || string(a.Data) != "mine" {
		t.Fatalf("got %+v %v, want the cookie for myhost:0", a, err)
	}
	a, _ = ReadXauthority(p, 0, "elsewhere")
	if a == nil || string(a.Data) != "other" {
		t.Fatalf("got %+v, want the first non-matching-host local/wild cookie for :0", a)
	}
	if a, err := ReadXauthority(filepath.Join(t.TempDir(), "missing"), 0, "h"); a != nil || err != nil {
		t.Fatalf("missing file: %+v %v, want nil,nil", a, err)
	}
}

func TestVKToKeysymAndKeymap(t *testing.T) {
	cases := map[int]uint32{0x41: 'a', 0x5A: 'z', 0x31: '1', 0x0D: 0xff0d, 0x70: 0xffbe, 0x7B: 0xffc9, 0x25: 0xff51, 0xBE: '.', 0x60: 0xffb0, 0x5B: 0xffeb}
	for vk, want := range cases {
		if got := VKToKeysym(vk); got != want {
			t.Errorf("VKToKeysym(0x%x) = 0x%x, want 0x%x", vk, got, want)
		}
	}
	// Keycode 10 carries 'a'/'A', 11 carries '!'/'1' (shifted first), 12 ISO_Level3_Shift.
	km := NewKeymap(10, 2, []uint32{'a', 'A', '!', '1', 0xfe03, 0})
	if c := km.Keycode('a'); c != 10 {
		t.Errorf("a -> %d", c)
	}
	if c := km.Keycode('1'); c != 11 {
		t.Errorf("1 -> %d, want 11 (found in the second column)", c)
	}
	if c := km.Keycode(0xffea); c != 12 {
		t.Errorf("Alt_R -> %d, want the ISO_Level3_Shift fallback 12", c)
	}
	if c := km.Keycode(0x1234); c != 0 {
		t.Errorf("unmapped keysym -> %d, want 0", c)
	}
}

func TestToBGRA(t *testing.T) {
	// One pixel R=0x11 G=0x22 B=0x33 in each layout.
	std := toBGRA([]byte{0x33, 0x22, 0x11, 0x00}, Visual{RedMask: 0xff0000, GreenMask: 0xff00, BlueMask: 0xff})
	rgb := toBGRA([]byte{0x11, 0x22, 0x33, 0x00}, Visual{RedMask: 0xff, GreenMask: 0xff00, BlueMask: 0xff0000})
	odd := toBGRA([]byte{0x00, 0x11 << 2, 0x00, 0x00}, Visual{RedMask: 0x3ff << 10, GreenMask: 0x3ff << 0, BlueMask: 0x3ff << 20})
	want := []byte{0x33, 0x22, 0x11, 0xff}
	if !bytes.Equal(std, want) || !bytes.Equal(rgb, want) {
		t.Fatalf("std %x rgb %x, want %x", std, rgb, want)
	}
	if odd[3] != 0xff {
		t.Fatalf("generic path left alpha %x", odd[3])
	}
}

func TestFindSessionsPrefersTheX11UserSession(t *testing.T) {
	root := t.TempDir()
	proc := func(pid, uid string, env map[string]string, cmdline ...string) {
		dir := filepath.Join(root, pid)
		_ = os.MkdirAll(dir, 0o755)
		var e []byte
		for k, v := range env {
			e = append(e, []byte(k+"="+v+"\x00")...)
		}
		_ = os.WriteFile(filepath.Join(dir, "environ"), e, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "status"), []byte("Name:\tx\nUid:\t"+uid+"\t"+uid+"\t"+uid+"\t"+uid+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "cmdline"), []byte(joinNul(cmdline)), 0o644)
	}
	proc("100", "0", nil, "/usr/lib/xorg/Xorg", "vt1", "-displayfd", "3", "-auth", "/run/user/120/gdm/Xauthority")
	proc("200", "120", map[string]string{"DISPLAY": ":0", "XAUTHORITY": "/run/user/120/gdm/Xauthority"}, "gdm-greeter")
	proc("300", "1000", map[string]string{"DISPLAY": ":1", "XAUTHORITY": "/run/user/1000/gdm/Xauthority", "XDG_SESSION_TYPE": "x11"}, "gnome-shell")
	proc("301", "1000", map[string]string{"DISPLAY": ":1", "XAUTHORITY": "/run/user/1000/gdm/Xauthority", "XDG_SESSION_TYPE": "x11"}, "firefox")
	proc("400", "0", map[string]string{"DISPLAY": "remote:0"}, "ssh")
	proc("self", "0", nil)

	s := FindSessions(root)
	if len(s.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want two (dupes and remote displays dropped)", s.Candidates)
	}
	if s.Candidates[0].Display != ":1" || s.Candidates[0].UID != 1000 {
		t.Fatalf("first candidate = %+v, want the user's X11 session on :1", s.Candidates[0])
	}
	if len(s.AuthFiles) != 1 || s.AuthFiles[0] != "/run/user/120/gdm/Xauthority" {
		t.Fatalf("auth files = %v", s.AuthFiles)
	}
	if s.Wayland {
		t.Fatal("reported Wayland on an X11 machine")
	}

	proc("500", "1000", map[string]string{"XDG_SESSION_TYPE": "wayland", "DISPLAY": ":0"}, "gnome-shell")
	if s := FindSessions(root); !s.Wayland {
		t.Fatal("a Wayland session was not noticed")
	}
}

func joinNul(args []string) string {
	var b bytes.Buffer
	for _, a := range args {
		b.WriteString(a)
		b.WriteByte(0)
	}
	return b.String()
}

// fakeServer answers the connection setup and then hands each request to the
// test, which writes whatever it wants back.
type fakeServer struct {
	t   *testing.T
	c   net.Conn
	seq uint16
}

func setupReply(width, height int) []byte {
	vendor := []byte("fake")
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	w(uint32(0))           // release
	w(uint32(0x00400000))  // resource-id-base
	w(uint32(0x001fffff))  // resource-id-mask
	w(uint32(0))           // motion buffer
	w(uint16(len(vendor))) // vendor length
	w(uint16(0xffff))      // max request length
	w(uint8(1))            // screens
	w(uint8(1))            // formats
	w([4]uint8{0, 0, 32, 32})
	w(uint8(8))   // min keycode
	w(uint8(255)) // max keycode
	w(uint32(0))
	b.Write(padded(vendor))
	w([8]uint8{24, 32, 32}) // depth 24 -> 32 bpp
	// screen
	w(uint32(0x100)) // root
	w(uint32(0x20))  // colormap
	w(uint32(0xffffff))
	w(uint32(0))
	w(uint32(0))
	w(uint16(width))
	w(uint16(height))
	w(uint16(300))
	w(uint16(200))
	w(uint16(1))
	w(uint16(1))
	w(uint32(0x21)) // root visual
	w([4]uint8{0, 0, 24, 1})
	// one depth with one visual
	w([8]uint8{24, 0, 1, 0})
	w(uint32(0x21))
	w([4]uint8{4, 8, 0, 1})
	w(uint32(0xff0000))
	w(uint32(0xff00))
	w(uint32(0xff))
	w(uint32(0))
	return b.Bytes()
}

func newFake(t *testing.T, width, height int) (*Conn, *fakeServer) {
	t.Helper()
	cli, srv := net.Pipe()
	t.Cleanup(func() { _ = cli.Close(); _ = srv.Close() })
	// A broken client must fail the test, not hang it: net.Pipe blocks a
	// write until the other side reads.
	_ = srv.SetDeadline(time.Now().Add(5 * time.Second))
	f := &fakeServer{t: t, c: srv}
	done := make(chan *Conn, 1)
	errc := make(chan error, 1)
	go func() {
		xc, err := NewConn(cli, &Auth{Name: cookieName, Data: []byte("0123456789abcdef")}, 0)
		if err != nil {
			errc <- err
			return
		}
		done <- xc
	}()
	head := make([]byte, 12)
	if _, err := io.ReadFull(srv, head); err != nil {
		t.Fatal(err)
	}
	if head[0] != 'l' || binary.LittleEndian.Uint16(head[2:]) != 11 {
		t.Fatalf("setup header %x", head)
	}
	nl, dl := int(binary.LittleEndian.Uint16(head[6:])), int(binary.LittleEndian.Uint16(head[8:]))
	auth := make([]byte, pad4(nl)+pad4(dl))
	if _, err := io.ReadFull(srv, auth); err != nil {
		t.Fatal(err)
	}
	if string(auth[:nl]) != cookieName || string(auth[pad4(nl):pad4(nl)+dl]) != "0123456789abcdef" {
		t.Fatalf("auth sent = %q", auth)
	}
	body := setupReply(width, height)
	reply := make([]byte, 8)
	reply[0] = 1
	binary.LittleEndian.PutUint16(reply[2:], 11)
	binary.LittleEndian.PutUint16(reply[6:], uint16(len(body)/4))
	if _, err := srv.Write(append(reply, body...)); err != nil {
		t.Fatal(err)
	}
	select {
	case xc := <-done:
		return xc, f
	case err := <-errc:
		t.Fatal(err)
	}
	return nil, nil
}

// request reads one request and returns its opcode, data byte and body.
func (f *fakeServer) request() (op, data byte, body []byte) {
	h := make([]byte, 4)
	if _, err := io.ReadFull(f.c, h); err != nil {
		f.t.Fatal(err)
	}
	n := int(binary.LittleEndian.Uint16(h[2:]))*4 - 4
	body = make([]byte, n)
	if _, err := io.ReadFull(f.c, body); err != nil {
		f.t.Fatal(err)
	}
	f.seq++
	return h[0], h[1], body
}

func (f *fakeServer) reply(data byte, rest []byte) {
	extra := 0
	if len(rest) > 24 {
		extra = pad4(len(rest)-24) / 4
	}
	b := make([]byte, 32+extra*4)
	b[0], b[1] = 1, data
	binary.LittleEndian.PutUint16(b[2:], f.seq)
	binary.LittleEndian.PutUint32(b[4:], uint32(extra))
	copy(b[8:], rest)
	if _, err := f.c.Write(b); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeServer) event(code byte) {
	b := make([]byte, 32)
	b[0] = code
	binary.LittleEndian.PutUint16(b[2:], f.seq)
	_, _ = f.c.Write(b)
}

func (f *fakeServer) xerror(code byte, seq uint16) {
	b := make([]byte, 32)
	b[1] = code
	binary.LittleEndian.PutUint16(b[2:], seq)
	b[10] = 77
	_, _ = f.c.Write(b)
}

func TestSetupParsesTheRootScreen(t *testing.T) {
	xc, _ := newFake(t, 300, 200)
	s := xc.Screen
	if s.Root != 0x100 || s.Width != 300 || s.Height != 200 || s.Depth != 24 || s.BPP != 32 {
		t.Fatalf("screen = %+v", s)
	}
	if s.Visual.RedMask != 0xff0000 || xc.MinKeycode != 8 || xc.MaxKeycode != 255 {
		t.Fatalf("visual %+v keycodes %d-%d", s.Visual, xc.MinKeycode, xc.MaxKeycode)
	}
}

func TestGetImageSkipsEventsAndConvertsPixels(t *testing.T) {
	xc, f := newFake(t, 300, 200)
	type res struct {
		img []byte
		err error
	}
	out := make(chan res, 1)
	go func() {
		img, err := xc.GetImage(10, 20, 2, 1)
		out <- res{img, err}
	}()
	op, data, body := f.request()
	if op != opGetImage || data != 2 {
		t.Fatalf("op %d format %d", op, data)
	}
	if x, y, w, h := int16(binary.LittleEndian.Uint16(body[4:])), int16(binary.LittleEndian.Uint16(body[6:])),
		binary.LittleEndian.Uint16(body[8:]), binary.LittleEndian.Uint16(body[10:]); x != 10 || y != 20 || w != 2 || h != 1 {
		t.Fatalf("rect %d,%d %dx%d", x, y, w, h)
	}
	f.event(12) // Expose: must be skipped
	f.event(34) // MappingNotify
	rest := make([]byte, 24+8)
	copy(rest[24:], []byte{1, 2, 3, 0, 4, 5, 6, 0})
	f.reply(24, rest)
	var r res
	select {
	case r = <-out:
	case <-time.After(5 * time.Second):
		t.Fatal("GetImage did not return")
	}
	if r.err != nil || !bytes.Equal(r.img, []byte{1, 2, 3, 0xff, 4, 5, 6, 0xff}) {
		t.Fatalf("img %v err %v", r.img, r.err)
	}
}

func TestErrorsReachTheirRequestAndVoidErrorsReachSync(t *testing.T) {
	xc, f := newFake(t, 300, 200)
	mapped := 0
	xc.OnMappingNotify = func() { mapped++ }
	errc := make(chan error, 1)
	go func() {
		_, _, _, err := xc.QueryPointer()
		errc <- err
	}()
	f.request()
	f.xerror(3, f.seq) // BadWindow for the QueryPointer itself
	var xerr *Error
	if err := <-errc; !errors.As(err, &xerr) || xerr.Code != 3 {
		t.Fatalf("QueryPointer error = %v, want BadWindow", err)
	}

	// A FakeInput fails; nothing waits for it, so Sync must report it.
	xc.ext["XTEST"] = extInfo{present: true, major: 140}
	go func() { errc <- xc.FakeInput(KeyPress, 38, 0, 0) }()
	op, minor, body := f.request()
	if op != 140 || minor != 2 || len(body) != 32 || body[0] != KeyPress || body[1] != 38 {
		t.Fatalf("FakeInput encoded as op %d minor %d body %x", op, minor, body)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	voidSeq := f.seq
	go func() { errc <- xc.Sync() }()
	f.request() // GetInputFocus
	f.xerror(2, voidSeq)
	f.event(34)
	f.reply(0, nil)
	if err := <-errc; !errors.As(err, &xerr) || xerr.Code != 2 {
		t.Fatalf("Sync = %v, want the BadValue from the FakeInput", err)
	}
	if mapped != 1 {
		t.Fatalf("MappingNotify callbacks = %d, want 1", mapped)
	}
}

func TestFakeInputMotionCarriesRootAndCoordinates(t *testing.T) {
	xc, f := newFake(t, 300, 200)
	xc.ext["XTEST"] = extInfo{present: true, major: 140}
	go func() { _ = xc.FakeInput(MotionNotify, 0, 123, -4) }()
	_, _, body := f.request()
	if body[0] != MotionNotify || binary.LittleEndian.Uint32(body[8:]) != 0x100 ||
		int16(binary.LittleEndian.Uint16(body[20:])) != 123 || int16(binary.LittleEndian.Uint16(body[22:])) != -4 {
		t.Fatalf("motion body %x", body)
	}
}

func TestRefusedConnectionReportsTheReason(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	go func() {
		h := make([]byte, 12)
		_, _ = io.ReadFull(srv, h)
		reason := padded([]byte("No protocol specified"))
		b := make([]byte, 8)
		b[1] = 21
		binary.LittleEndian.PutUint16(b[6:], uint16(len(reason)/4))
		_, _ = srv.Write(append(b, reason...))
	}()
	_, err := NewConn(cli, nil, 0)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("No protocol specified")) {
		t.Fatalf("err = %v", err)
	}
}

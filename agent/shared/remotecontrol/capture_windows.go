//go:build windows

package remotecontrol

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procGetCurrentThreadID   = kernel32.NewProc("GetCurrentThreadId")
	procGetCurrentProcessId  = kernel32.NewProc("GetCurrentProcessId")
	procProcessIdToSessionId = kernel32.NewProc("ProcessIdToSessionId")

	procOpenInputDesktop          = user32.NewProc("OpenInputDesktop")
	procSetThreadDesktop          = user32.NewProc("SetThreadDesktop")
	procGetThreadDesktop          = user32.NewProc("GetThreadDesktop")
	procGetUserObjectInformationW = user32.NewProc("GetUserObjectInformationW")
	procCloseDesktop              = user32.NewProc("CloseDesktop")

	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procGetDC              = user32.NewProc("GetDC")
	procReleaseDC          = user32.NewProc("ReleaseDC")
	procSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")
	procSetCursorPos       = user32.NewProc("SetCursorPos")
	procSendInput          = user32.NewProc("SendInput")
	procMapVirtualKeyExW   = user32.NewProc("MapVirtualKeyExW")
	procGetKeyboardLayout  = user32.NewProc("GetKeyboardLayout")

	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procBitBlt             = gdi32.NewProc("BitBlt")
	procGdiFlush           = gdi32.NewProc("GdiFlush")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
)

const (
	SM_CXSCREEN    = 0
	SM_CYSCREEN    = 1
	SRCCOPY        = 0x00CC0020
	CAPTUREBLT     = 0x40000000
	BI_RGB         = 0
	DIB_RGB_COLORS = 0

	// SendInput, not the legacy mouse_event/keybd_event. Both are documented as
	// superseded, and the legacy pair is where the interesting bugs live: they
	// take a virtual-key code and let the system guess, so an extended key such
	// as an arrow, the numpad, or right Ctrl never gets KEYEVENTF_EXTENDEDKEY
	// and arrives as a different key or not at all.
	INPUT_MOUSE    = 0
	INPUT_KEYBOARD = 1

	KEYEVENTF_EXTENDEDKEY = 0x0001
	KEYEVENTF_KEYUP       = 0x0002
	KEYEVENTF_SCANCODE    = 0x0008

	MOUSEEVENTF_MOVE       = 0x0001
	MOUSEEVENTF_LEFTDOWN   = 0x0002
	MOUSEEVENTF_LEFTUP     = 0x0004
	MOUSEEVENTF_RIGHTDOWN  = 0x0008
	MOUSEEVENTF_RIGHTUP    = 0x0010
	MOUSEEVENTF_MIDDLEDOWN = 0x0020
	MOUSEEVENTF_MIDDLEUP   = 0x0040
	MOUSEEVENTF_WHEEL      = 0x0800
	MOUSEEVENTF_HWHEEL     = 0x1000

	MAPVK_VK_TO_VSC_EX = 4

	// uoiName asks GetUserObjectInformation for an object's name.
	uoiName = 2

	// desktopAll is the access mask OpenInputDesktop asks for. Read/write on the
	// object list is the documented mask for this call; the specific values
	// matter only in that they must be non-zero.
	desktopAll = 0x01FF
)

// win32Input matches tagINPUT on 64-bit Windows:
// 4 bytes type, 4 bytes padding, 32 bytes union. Total size: 40 bytes.
//
// The union is declared as uint64s so it is 8-byte aligned: the mouse and
// keyboard records written into it hold a uintptr.
type win32Input struct {
	Type uint32
	_pad uint32
	Data [4]uint64
}

type mouseInput struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	_pad        uint32
	DwExtraInfo uintptr
}

type keyboardInput struct {
	Vk          uint16
	Scan        uint16
	DwFlags     uint32
	Time        uint32
	_pad        uint32
	DwExtraInfo uintptr
	_trailing   uint64
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

// bitmapInfo is BITMAPINFO: the header plus the one-entry colour table the
// struct declares. CreateDIBSection reads a BITMAPINFO, not a bare header, so
// the trailing slot keeps the call inside memory we own.
type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

// dibSurface is the memory DC and the DIB section selected into it that every
// frame is blitted into.
//
// The capture used to blit into a CreateCompatibleBitmap device bitmap and read
// it back with GetDIBits. That readback is the step that failed on endpoints --
// "getdibits failed" with a Win32 error of 0 -- after BitBlt had succeeded, and
// it failed persistently once it started: a device bitmap lives wherever the
// display driver puts it, and converting a full-screen one back to a DIB is a
// driver operation that a basic or virtual display adapter, or one short of
// memory, refuses without saying why. It also cost an 8MB device allocation on
// every tick.
//
// A DIB section is system memory GDI owns and draws into directly, so BitBlt
// writes the pixels straight into the buffer we read and there is no readback
// at all. It is what VNC servers capture into for the same reason. It is
// top-down (negative height), so rows arrive in the order Damage.Diff expects
// and nothing has to be flipped.
type dibSurface struct {
	hdcMem  uintptr
	hBitmap uintptr
	hOld    uintptr
	bits    unsafe.Pointer
	width   int
	height  int
}

func newDIBSurface(hdcScreen uintptr, width, height int) (*dibSurface, error) {
	hdcMem, _, callErr := procCreateCompatibleDC.Call(hdcScreen)
	if hdcMem == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed: %w", cleanWin32Err(callErr))
	}

	var bi bitmapInfo
	bi.Header.BiSize = uint32(unsafe.Sizeof(bi.Header))
	bi.Header.BiWidth = int32(width)
	bi.Header.BiHeight = -int32(height) // negative: top-down rows
	bi.Header.BiPlanes = 1
	bi.Header.BiBitCount = 32
	bi.Header.BiCompression = BI_RGB

	var bits unsafe.Pointer
	hBitmap, _, callErr := procCreateDIBSection.Call(
		hdcScreen,
		uintptr(unsafe.Pointer(&bi)),
		DIB_RGB_COLORS,
		uintptr(unsafe.Pointer(&bits)),
		0,
		0,
	)
	if hBitmap == 0 || bits == nil {
		procDeleteDC.Call(hdcMem)
		return nil, fmt.Errorf("CreateDIBSection %dx%d failed: %w", width, height, cleanWin32Err(callErr))
	}

	hOld, _, _ := procSelectObject.Call(hdcMem, hBitmap)
	return &dibSurface{
		hdcMem:  hdcMem,
		hBitmap: hBitmap,
		hOld:    hOld,
		bits:    bits,
		width:   width,
		height:  height,
	}, nil
}

// close deselects the section before deleting it. DeleteObject refuses a bitmap
// that is still selected into a DC, and the refusal is silent: the 8MB section
// would simply never be freed.
func (s *dibSurface) close() {
	procSelectObject.Call(s.hdcMem, s.hOld)
	procDeleteObject.Call(s.hBitmap)
	procDeleteDC.Call(s.hdcMem)
}

// blit copies the screen into the section and waits for GDI to finish writing
// it. GDI batches drawing calls per thread, and the section's bits are only
// guaranteed to hold the blit once the batch is flushed.
func (s *dibSurface) blit(hdcScreen uintptr, srcX, srcY int) error {
	ret, _, callErr := procBitBlt.Call(s.hdcMem, 0, 0, uintptr(s.width), uintptr(s.height), hdcScreen, uintptr(srcX), uintptr(srcY), uintptr(SRCCOPY|CAPTUREBLT))
	if ret == 0 {
		return cleanWin32Err(callErr)
	}
	procGdiFlush.Call()
	return nil
}

type WindowsCapturer struct {
	// mu guards held. The capturer is one object shared by the capability probe,
	// the capture loop and the input loop, which run on three different
	// goroutines, and InjectKeyboardEvent writes here from the relay's read
	// goroutine while ReleaseAllKeys iterates from Stop. Without the lock that
	// is a concurrent map write, which is a runtime throw, not a lost update --
	// and it only shows up under load, in production, on the machine being
	// administered.
	mu   sync.Mutex
	held map[uint16]bool
	// buttons are the mouse buttons the operator pressed and has not released,
	// so a release-all lifts them too. A button let go outside the console's
	// canvas used to stay down on the endpoint, dragging everything after it.
	buttons map[string]bool

	// capMu guards surf. The surface is reused from frame to frame and is only
	// rebuilt when the resolution changes; two captures blitting into the same
	// section at once would hand each of them a mixture of both frames.
	capMu sync.Mutex
	surf  *dibSurface

	// geoMu guards the monitor selection and the geometry of the last frame.
	// Input runs on the relay's read goroutine and has to translate the
	// operator's frame coordinates by the origin of whatever is being captured;
	// sharing capMu would make every mouse move wait out a full-screen blit.
	geoMu        sync.Mutex
	selected     int
	originX      int
	originY      int
	areaW, areaH int
}

func NewPlatformCapturer() ScreenCapturer {
	// Opt into DPI awareness before the first GetSystemMetrics. A process that
	// stays DPI-unaware is handed virtualised (scaled) screen metrics, so
	// SetCursorPos is asked to move to a point that does not exist in the
	// coordinate space the frame was captured in: the operator's cursor lands
	// progressively further from the point they clicked, further right and
	// further down the screen the longer the session runs.
	//
	// Safe to call more than once and safe to fail: on an already-aware process
	// it returns 0 and changes nothing.
	_, _, _ = procSetProcessDPIAware.Call()

	// Attach before the first probe, not after it: a process launched without
	// a desktop has nothing to blit from, so probeCapture would report the
	// capability as missing when the fix is a single call away.
	_ = attachToInputDesktop()

	return &WindowsCapturer{held: make(map[uint16]bool), buttons: make(map[string]bool), selected: monitorPrimary}
}

// attachToInputDesktop gives the calling thread a desktop to draw from.
//
// A process started without a desktop -- an agent launched by a scheduled task
// or a service, or any session-0 process -- still inherits the interactive
// window station, so GetDC(NULL) returns a real handle and GetSystemMetrics
// reports the real resolution. Only the blit fails, with ERROR_INVALID_HANDLE.
// The capture therefore reported the host as unable to capture when the host was
// in fact a normal desktop that this process had simply never been attached to.
//
// OpenInputDesktop followed by SetThreadDesktop is the documented repair, and it
// is what .NET's CopyFromScreen also needs: in the same process context
// CopyFromScreen failed with the identical error, which is what ruled out a
// Go-specific defect and pointed at the missing attachment.
//
// A thread can only be attached once, and only before it has created any
// windows, so the attach is recorded per thread: CaptureScreen runs on the
// streaming goroutine, not on the one that built the capturer, and attaching
// only the constructor's thread would leave the capture goroutine with the same
// empty desktop it started with.
//
// A failure is not fatal here. An already-attached thread, a host with no
// interactive desktop, and a locked workstation all refuse the call, and in
// each of those the blit below is what decides whether streaming is possible --
// so the caller inspects the blit, not this error.
var attachedThreads sync.Map

// attachToInputDesktop gives the calling thread a desktop to draw from.
func attachToInputDesktop() error {
	tid, _, _ := procGetCurrentThreadID.Call()
	if _, done := attachedThreads.Load(tid); done {
		return nil
	}

	hDesktop, _, callErr := procOpenInputDesktop.Call(0, 0, desktopAll)
	if hDesktop == 0 {
		return fmt.Errorf("open input desktop: %w", callErr)
	}

	if ret, _, callErr := procSetThreadDesktop.Call(hDesktop); ret == 0 {
		procCloseDesktop.Call(hDesktop)
		return fmt.Errorf("set thread desktop: %w", callErr)
	}
	attachedThreads.Store(tid, hDesktop)
	return nil
}

// reattachInputDesktop moves the calling thread onto whatever desktop is the
// input desktop now, after a blit has failed.
//
// The input desktop changes under a running session -- a UAC prompt, Ctrl+Alt+Del
// or the lock screen all switch it -- and a thread still attached to the old one
// gets "Access is denied" from every blit. This used to forget the thread and
// CloseDesktop the handle it was still attached to; Windows refuses to close a
// desktop a thread is using, so the handle leaked on every failed tick and the
// next attach opened another. The old handle is closed only once the thread has
// actually moved off it.
func reattachInputDesktop() {
	tid, _, _ := procGetCurrentThreadID.Call()
	hDesktop, _, _ := procOpenInputDesktop.Call(0, 0, desktopAll)
	if hDesktop == 0 {
		// The input desktop is one this user cannot open (the secure desktop
		// behind a lock or UAC prompt). Stay where we are: the next tick tries
		// again, and succeeds once the user is back on their own desktop.
		return
	}
	if ret, _, _ := procSetThreadDesktop.Call(hDesktop); ret == 0 {
		procCloseDesktop.Call(hDesktop)
		return
	}
	if prev, ok := attachedThreads.Swap(tid, hDesktop); ok {
		if old, ok := prev.(uintptr); ok && old != 0 && old != hDesktop {
			procCloseDesktop.Call(old)
		}
	}
}

// syncInputDesktop moves the calling thread onto the current input desktop if
// it has changed since the thread was attached.
//
// Input used to attach once per thread and never again. When the input desktop
// switched -- a UAC prompt, Ctrl+Alt+Del, the lock screen -- SendInput kept
// targeting the old desktop and was silently ignored, so the operator could see
// the prompt (capture re-attaches on a failed blit) but not click it. Comparing
// names costs two calls per event, which is nothing at input rates.
func syncInputDesktop() {
	tid, _, _ := procGetCurrentThreadID.Call()
	if _, done := attachedThreads.Load(tid); !done {
		_ = attachToInputDesktop()
		return
	}
	hInput, _, _ := procOpenInputDesktop.Call(0, 0, desktopAll)
	if hInput == 0 {
		return // a desktop this process may not open; stay where we are
	}
	cur, _, _ := procGetThreadDesktop.Call(tid)
	if cur != 0 && desktopName(cur) == desktopName(hInput) {
		procCloseDesktop.Call(hInput)
		return
	}
	if ret, _, _ := procSetThreadDesktop.Call(hInput); ret == 0 {
		procCloseDesktop.Call(hInput)
		return
	}
	if prev, ok := attachedThreads.Swap(tid, hInput); ok {
		if old, ok := prev.(uintptr); ok && old != 0 && old != hInput {
			procCloseDesktop.Call(old)
		}
	}
}

// desktopName returns a desktop's name ("Default", "Winlogon", ...), or "" when
// it cannot be read.
func desktopName(h uintptr) string {
	var buf [64]uint16
	var needed uint32
	r, _, _ := procGetUserObjectInformationW.Call(h, uoiName, uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)*2), uintptr(unsafe.Pointer(&needed)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:])
}

// sessionZeroHint names the one cause of a failed blit that no retry fixes: the
// process is in Session 0, which has no desktop a person is looking at.
func sessionZeroHint() string {
	var sessionID uint32
	pid, _, _ := procGetCurrentProcessId.Call()
	if r, _, _ := procProcessIdToSessionId.Call(pid, uintptr(unsafe.Pointer(&sessionID))); r != 0 && sessionID == 0 {
		return " (running in Windows Session 0: workstation is locked or has no active logged-on user session)"
	}
	return ""
}

// Capabilities reports what this host can actually do, not what Windows can do
// in general. The input paths go through SendInput, which is present on every
// supported version, so neither of those is a stub here.
//
// Capture is probed with a real 1x1 BitBlt rather than assumed. Probing GetDC
// is not enough: on a host with no interactive window station GetDC still
// returns a valid handle and real screen metrics, and only the blit itself
// fails with ERROR_INVALID_HANDLE. A 1x1 blit costs microseconds and is the
// only test that tells the truth, so Capabilities never claims a capability
// the host cannot deliver.
func (c *WindowsCapturer) Capabilities() Capabilities {
	caps := Capabilities{}
	if err := c.probeCapture(); err != nil {
		caps.Reason = "screen capture is unavailable on this host: " + err.Error()
		return caps
	}
	// Mouse and keyboard are claimed here, not before the probe, because
	// claiming them on the strength of "SendInput exists" is what made the hello
	// frame a lie. Both inject into the calling session's input queue, so a host
	// that cannot be blitted cannot be typed at either. Asserting them first gave
	// an endpoint that advertised "no capture, but mouse and keyboard work" -- and
	// the console has no way to show a capability the agent never tested.
	caps.Capture = true
	caps.Mouse = true
	caps.Keyboard = true
	return caps
}

func cleanWin32Err(callErr error) error {
	if callErr == nil || callErr == syscall.Errno(0) {
		return errors.New("win32 error code 0 (display driver or DC format rejected DIB)")
	}
	return callErr
}

// probeCapture performs a 1x1 blit through the same DIB-section path the real
// capture uses, and reports why it failed, if it did.
func (c *WindowsCapturer) probeCapture() error {
	// The probe is the thing that decides whether the console is told the host
	// can capture, so it has to run on an attached thread. Probing from a
	// thread with no desktop reported a perfectly capable machine as unable to
	// capture, and the operator was shown a dead session with no reason.
	_ = attachToInputDesktop()

	hdcScreen, _, _ := procGetDC.Call(0)
	if hdcScreen == 0 {
		return errors.New("GetDC(NULL) returned a null screen device context")
	}
	defer procReleaseDC.Call(0, hdcScreen)

	surf, err := newDIBSurface(hdcScreen, 1, 1)
	if err != nil {
		return err
	}
	defer surf.close()

	if err := surf.blit(hdcScreen, 0, 0); err != nil {
		reattachInputDesktop()
		return fmt.Errorf("BitBlt failed: %w%s", err, sessionZeroHint())
	}
	return nil
}

func (c *WindowsCapturer) CaptureScreen() ([]byte, int, int, error) {
	// This runs on the streaming goroutine, which inherited no desktop of its
	// own, so it attaches before asking for a screen DC. Without this the blit
	// below fails with ERROR_INVALID_HANDLE and the operator gets an empty
	// desktop with no explanation.
	_ = attachToInputDesktop()

	area := c.captureRect()
	width, height := area.Width, area.Height
	if width <= 0 || height <= 0 {
		return nil, 0, 0, fmt.Errorf("invalid screen dimensions: %dx%d", width, height)
	}

	hdcScreen, _, _ := procGetDC.Call(0)
	if hdcScreen == 0 {
		return nil, 0, 0, fmt.Errorf("failed to get screen DC")
	}
	defer procReleaseDC.Call(0, hdcScreen)

	c.capMu.Lock()
	defer c.capMu.Unlock()

	// The surface survives between frames and is rebuilt only when the
	// resolution changes, so a steady session allocates nothing in GDI per tick.
	if c.surf == nil || c.surf.width != width || c.surf.height != height {
		if c.surf != nil {
			c.surf.close()
			c.surf = nil
		}
		surf, err := newDIBSurface(hdcScreen, width, height)
		if err != nil {
			return nil, 0, 0, err
		}
		c.surf = surf
	}

	if err := c.surf.blit(hdcScreen, area.X, area.Y); err != nil {
		// The Win32 code is kept deliberately. A non-interactive window station
		// (a service, a CI runner, a container) makes BitBlt fail with a code
		// that no amount of code change will fix, and without it this error is
		// indistinguishable from a genuine capture bug.
		reattachInputDesktop()
		return nil, 0, 0, fmt.Errorf("bitblt failed on a %dx%d blit: %w%s", width, height, err, sessionZeroHint())
	}

	// The section is overwritten by the next blit, and Damage keeps the frame it
	// is handed as its baseline, so the pixels are copied out rather than lent.
	//
	// They stay BGRA. Converting to RGBA and encoding here is what made this a
	// polled screenshot: both are full passes over 8MB spent on the whole desktop
	// whether or not anything changed. Damage.Diff works on these bytes directly,
	// and only the rectangles it decides to send get converted and encoded.
	size := width * height * 4
	pixels := make([]byte, size)
	copy(pixels, unsafe.Slice((*byte)(c.surf.bits), size))
	return pixels, width, height, nil
}

// releaseCaptureResources frees the cached surface when a session ends. The
// service process keeps one capturer for its whole life, and without this an
// 8MB section from a fallback session would stay allocated until the agent
// restarts.
func (c *WindowsCapturer) releaseCaptureResources() {
	c.capMu.Lock()
	defer c.capMu.Unlock()
	if c.surf != nil {
		c.surf.close()
		c.surf = nil
	}
}

func (c *WindowsCapturer) InjectMouseEvent(e InputEvent) error {
	syncInputDesktop()

	// SetCursorPos for the move, SendInput for the button and wheel events.
	// Both operate in the same virtual-desktop coordinate space the frame was
	// captured in, because the process is now DPI-aware.
	// The console's coordinates are relative to the captured frame, which is
	// not at the virtual desktop's origin once a secondary monitor -- or a
	// monitor left of the primary, at negative coordinates -- is selected.
	if e.X >= 0 && e.Y >= 0 {
		ox, oy := c.origin()
		// The result is checked: a cursor that cannot be placed means every
		// click after it lands somewhere else, and that is worth reporting.
		if r, _, callErr := procSetCursorPos.Call(uintptr(e.X+ox), uintptr(e.Y+oy)); r == 0 {
			return fmt.Errorf("SetCursorPos(%d,%d) failed: %w", e.X+ox, e.Y+oy, cleanWin32Err(callErr))
		}
	}

	var flags uint32
	var mouseData uint32
	switch e.Action {
	case "move":
		// SetCursorPos already moved the cursor to the target coordinate and generated
		// WM_MOUSEMOVE.
		return nil
	case "down":
		switch e.Button {
		case "right":
			flags = MOUSEEVENTF_RIGHTDOWN
		case "middle":
			flags = MOUSEEVENTF_MIDDLEDOWN
		default:
			flags = MOUSEEVENTF_LEFTDOWN
		}
	case "up":
		switch e.Button {
		case "right":
			flags = MOUSEEVENTF_RIGHTUP
		case "middle":
			flags = MOUSEEVENTF_MIDDLEUP
		default:
			flags = MOUSEEVENTF_LEFTUP
		}
	case "wheel":
		flags = MOUSEEVENTF_WHEEL
		mouseData = uint32(int32(e.Delta))
	case "hwheel":
		// Positive is to the right, the same convention WM_MOUSEHWHEEL uses.
		flags = MOUSEEVENTF_HWHEEL
		mouseData = uint32(int32(e.Delta))
	default:
		return nil
	}

	button := e.Button
	if button == "" {
		button = "left"
	}
	c.mu.Lock()
	switch e.Action {
	case "down":
		c.buttons[button] = true
	case "up":
		delete(c.buttons, button)
	}
	c.mu.Unlock()

	// Dx=0, Dy=0 because cursor is already positioned exactly at (e.X, e.Y) by SetCursorPos.
	mi := mouseInput{
		Dx:        0,
		Dy:        0,
		MouseData: mouseData,
		DwFlags:   flags,
	}

	return c.sendMouseInput(mi)
}

// isExtendedVK reports whether a virtual key needs KEYEVENTF_EXTENDEDKEY.
// Without the flag the system reads the high bit of the scan code to decide
// extended-ness, so an unflagged arrow key is delivered as the numpad arrow of
// the same scan code, and right-hand modifiers cannot be distinguished from
// left-hand ones at all.
//
// 0xBA used to be listed. It is VK_OEM_1 -- the ';:' key on a US layout -- and
// sending it as E0 27 produced no character at all.
func isExtendedVK(vk uint16) bool {
	switch {
	case vk >= 0x21 && vk <= 0x2E, // PgUp/PgDn, End/Home, arrows, PrintScreen, Insert/Delete
		vk == 0x5B || vk == 0x5C, // LWin, RWin
		vk == 0x5D,               // Apps
		vk == 0x6F,               // numpad divide
		vk == 0x90,               // NumLock
		vk == 0xA3, vk == 0xA5,   // RCtrl, RAlt
		vk >= 0xA6 && vk <= 0xB7: // browser, volume and media keys
		return true
	}
	return false
}

// keyFlags builds the scan code and flags for one key record. numpad marks a
// key the browser reported from the keypad (KeyboardEvent.location 3), which
// is how numpad Enter is told apart from the main Enter: both are VK_RETURN.
func keyFlags(vk uint16, numpad bool, layout uintptr) (uint16, uint32) {
	var scan uint16
	if ret, _, _ := procMapVirtualKeyExW.Call(uintptr(vk), uintptr(MAPVK_VK_TO_VSC_EX), layout); ret != 0 {
		scan = uint16(ret)
	}
	flags := uint32(0)
	if isExtendedVK(vk) || (numpad && vk == 0x0D) || (scan&0xFF00) == 0xE000 || (scan&0xFF00) == 0xE100 {
		flags |= KEYEVENTF_EXTENDEDKEY
		scan = scan & 0xFF
	}
	if scan != 0 {
		flags |= KEYEVENTF_SCANCODE
	}
	return scan, flags
}

func (c *WindowsCapturer) InjectKeyboardEvent(e InputEvent) error {
	if e.Code <= 0 {
		return nil
	}
	vk := uint16(e.Code)
	syncInputDesktop()

	// The virtual key is translated to a scan code through the endpoint's
	// keyboard layout, so the key at the same physical position is pressed.
	layout, _, _ := procGetKeyboardLayout.Call(0)
	scan, flags := keyFlags(vk, e.Location == 3, layout)
	if e.Action == "up" {
		flags |= KEYEVENTF_KEYUP
	}

	// Scan path when the layout produced one, virtual-key path otherwise. Both
	// carry Vk, so a target application sees a complete key record either way.
	ki := keyboardInput{Vk: vk, Scan: scan, DwFlags: flags}
	err := c.sendKeyboardInput(ki)

	c.mu.Lock()
	if e.Action == "up" {
		delete(c.held, vk)
	} else {
		c.held[vk] = true
	}
	c.mu.Unlock()
	return err
}

func (c *WindowsCapturer) ReleaseAllKeys() error {
	// The set is copied under the lock and the sends happen outside it:
	// SendInput is a syscall that can block, and holding the lock across it
	// would serialise every keystroke behind a stuck input queue. The copy is
	// what makes it safe -- a key that goes up while this runs is simply
	// released twice, which the input layer discards.
	c.mu.Lock()
	pending := make([]uint16, 0, len(c.held))
	for vk := range c.held {
		pending = append(pending, vk)
	}
	c.held = make(map[uint16]bool)
	buttons := make([]string, 0, len(c.buttons))
	for b := range c.buttons {
		buttons = append(buttons, b)
	}
	c.buttons = make(map[string]bool)
	c.mu.Unlock()

	syncInputDesktop()
	// Released with the same layout the keys were pressed with; layout 0 used
	// to map some keys to a different scan code than their keydown had.
	layout, _, _ := procGetKeyboardLayout.Call(0)
	for _, vk := range pending {
		scan, flags := keyFlags(vk, false, layout)
		ki := keyboardInput{Vk: vk, Scan: scan, DwFlags: flags | KEYEVENTF_KEYUP}
		_ = c.sendKeyboardInput(ki)
	}
	for _, b := range buttons {
		up := uint32(MOUSEEVENTF_LEFTUP)
		switch b {
		case "right":
			up = MOUSEEVENTF_RIGHTUP
		case "middle":
			up = MOUSEEVENTF_MIDDLEUP
		}
		_ = c.sendMouseInput(mouseInput{DwFlags: up})
	}
	return nil
}

func (c *WindowsCapturer) sendMouseInput(mi mouseInput) error {
	var in win32Input
	in.Type = INPUT_MOUSE
	*(*mouseInput)(unsafe.Pointer(&in.Data)) = mi
	ret, _, callErr := procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
	if ret == 0 {
		return fmt.Errorf("SendInput mouse failed: %w", callErr)
	}
	return nil
}

func (c *WindowsCapturer) sendKeyboardInput(ki keyboardInput) error {
	var in win32Input
	in.Type = INPUT_KEYBOARD
	*(*keyboardInput)(unsafe.Pointer(&in.Data)) = ki
	ret, _, callErr := procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
	if ret == 0 {
		return fmt.Errorf("SendInput keyboard failed: %w", callErr)
	}
	return nil
}

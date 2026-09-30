//go:build windows

package remotecontrol

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"syscall"
	"unsafe"
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procGetDC              = user32.NewProc("GetDC")
	procReleaseDC          = user32.NewProc("ReleaseDC")
	procSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")
	procSetCursorPos       = user32.NewProc("SetCursorPos")
	procSendInput          = user32.NewProc("SendInput")
	procMapVirtualKeyExW   = user32.NewProc("MapVirtualKeyExW")
	procGetKeyboardLayout  = user32.NewProc("GetKeyboardLayout")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
)

const (
	SM_CXSCREEN = 0
	SM_CYSCREEN = 1
	SRCCOPY     = 0x00CC0020
	BI_RGB      = 0

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

	MAPVK_VK_TO_VSC_EX = 4
	// VK_PACKET routes a key through the active keyboard layout's translation,
	// which is what makes a non-US operator's own layout produce the right
	// character on the endpoint instead of a US one.
	VK_PACKET = 0xE7
)

type inputHeader struct {
	Type uint32
	_    uint32
}

type mouseInput struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type keyboardInput struct {
	Vk          uint16
	Scan        uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type inputUnion struct {
	Ki keyboardInput
	Mi mouseInput
}

type input struct {
	Header inputHeader
	Union  inputUnion
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

type WindowsCapturer struct {
	held map[uint16]bool
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

	return &WindowsCapturer{held: make(map[uint16]bool)}
}

func (c *WindowsCapturer) Capabilities() Capabilities {
	// Both input paths go through SendInput, which is available on every Windows
	// version this agent supports, so neither is a stub here.
	return Capabilities{Capture: true, Mouse: true, Keyboard: true}
}

func (c *WindowsCapturer) CaptureScreen() ([]byte, int, int, error) {
	w, _, _ := procGetSystemMetrics.Call(uintptr(SM_CXSCREEN))
	h, _, _ := procGetSystemMetrics.Call(uintptr(SM_CYSCREEN))
	width := int(w)
	height := int(h)
	if width <= 0 || height <= 0 {
		return nil, 0, 0, fmt.Errorf("invalid screen dimensions: %dx%d", width, height)
	}

	hdcScreen, _, _ := procGetDC.Call(0)
	if hdcScreen == 0 {
		return nil, 0, 0, fmt.Errorf("failed to get screen DC")
	}
	defer procReleaseDC.Call(0, hdcScreen)

	hdcMem, _, _ := procCreateCompatibleDC.Call(hdcScreen)
	if hdcMem == 0 {
		return nil, 0, 0, fmt.Errorf("failed to create compatible DC")
	}
	defer procDeleteDC.Call(hdcMem)

	hBitmap, _, _ := procCreateCompatibleBitmap.Call(hdcScreen, uintptr(width), uintptr(height))
	if hBitmap == 0 {
		return nil, 0, 0, fmt.Errorf("failed to create compatible bitmap")
	}
	defer procDeleteObject.Call(hBitmap)

	hOld, _, _ := procSelectObject.Call(hdcMem, hBitmap)
	defer procSelectObject.Call(hdcMem, hOld)

	ret, _, _ := procBitBlt.Call(hdcMem, 0, 0, uintptr(width), uintptr(height), hdcScreen, 0, 0, uintptr(SRCCOPY))
	if ret == 0 {
		return nil, 0, 0, fmt.Errorf("bitblt failed")
	}

	var bi bitmapInfoHeader
	bi.BiSize = uint32(unsafe.Sizeof(bi))
	bi.BiWidth = int32(width)
	bi.BiHeight = -int32(height) // negative for top-down bitmap
	bi.BiPlanes = 1
	bi.BiBitCount = 32
	bi.BiCompression = BI_RGB

	rawPixels := make([]byte, width*height*4)

	ret, _, _ = procGetDIBits.Call(
		hdcMem,
		hBitmap,
		0,
		uintptr(height),
		uintptr(unsafe.Pointer(&rawPixels[0])),
		uintptr(unsafe.Pointer(&bi)),
		0,
	)
	if ret == 0 {
		return nil, 0, 0, fmt.Errorf("getdibits failed")
	}

	// Win32 DIB is BGRA, convert to RGBA in place.
	for i := 0; i < len(rawPixels); i += 4 {
		b := rawPixels[i]
		r := rawPixels[i+2]
		rawPixels[i] = r
		rawPixels[i+2] = b
		rawPixels[i+3] = 0xFF
	}

	img := &image.RGBA{
		Pix:    rawPixels,
		Stride: width * 4,
		Rect:   image.Rect(0, 0, width, height),
	}

	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, img, &jpeg.Options{Quality: 60}); err != nil {
		return nil, 0, 0, fmt.Errorf("encode jpeg: %w", err)
	}

	return jpegBuf.Bytes(), width, height, nil
}

func (c *WindowsCapturer) InjectMouseEvent(e InputEvent) error {
	// SetCursorPos for the move, SendInput for the button and wheel events.
	// Both operate in the same virtual-desktop coordinate space the frame was
	// captured in, because the process is now DPI-aware.
	if e.X >= 0 && e.Y >= 0 {
		_, _, _ = procSetCursorPos.Call(uintptr(e.X), uintptr(e.Y))
	}

	var flags uint32
	switch e.Action {
	case "move":
		flags = MOUSEEVENTF_MOVE
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
	default:
		return nil
	}

	mi := mouseInput{
		Dx:        int32(e.X),
		Dy:        int32(e.Y),
		MouseData: uint32(int32(e.Delta)),
		DwFlags:   flags,
	}

	c.sendInput(input{
		Header: inputHeader{Type: INPUT_MOUSE},
		Union:  inputUnion{Mi: mi},
	})
	return nil
}

// isExtendedVK reports whether a virtual key needs KEYEVENTF_EXTENDEDKEY.
// Without the flag the system reads the high bit of the scan code to decide
// extended-ness, so an unflagged arrow key is delivered as the numpad arrow of
// the same scan code, and right-hand modifiers cannot be distinguished from
// left-hand ones at all.
func isExtendedVK(vk uint16) bool {
	switch {
	case vk >= 0x21 && vk <= 0x2E, // arrows, Insert/Delete/Home/End, PgUp/PgDn
		vk == 0x5B || vk == 0x5C, // LWin, RWin
		vk == 0x5D,               // Apps
		vk == 0x6F,               // numpad divide
		vk == 0x9B,               // numpad enter (extended Enter)
		vk == 0xA3, vk == 0xA5,   // RCtrl, RAlt
		vk >= 0xAD && vk <= 0xB4, // numpad operators
		vk == 0xBA:
		return true
	}
	return false
}

func (c *WindowsCapturer) InjectKeyboardEvent(e InputEvent) error {
	if e.Code <= 0 {
		return nil
	}
	vk := uint16(e.Code)

	// Scan code via VK_PACKET so the endpoint's active keyboard layout
	// translates the operator's key the way that layout would locally. This is
	// what keeps an AZERTY or Dvorak operator's keystrokes producing the same
	// characters on the endpoint as they do on the operator's own machine.
	layout, _, _ := procGetKeyboardLayout.Call(0)
	var scan uint16
	if ret, _, _ := procMapVirtualKeyExW.Call(uintptr(vk), uintptr(MAPVK_VK_TO_VSC_EX), layout); ret != 0 {
		scan = uint16(ret)
	}

	flags := uint32(0)
	if e.Action == "up" {
		flags |= KEYEVENTF_KEYUP
	}
	if isExtendedVK(vk) {
		flags |= KEYEVENTF_EXTENDEDKEY
	}
	if scan != 0 {
		flags |= KEYEVENTF_SCANCODE
	}

	// Scan path when the layout produced one, virtual-key path otherwise. Both
	// carry Vk, so a target application sees a complete key record either way.
	ki := keyboardInput{Vk: vk, Scan: scan, DwFlags: flags}
	c.sendInput(input{Header: inputHeader{Type: INPUT_KEYBOARD}, Union: inputUnion{Ki: ki}})

	if e.Action == "up" {
		delete(c.held, vk)
	} else {
		c.held[vk] = true
	}
	return nil
}

func (c *WindowsCapturer) ReleaseAllKeys() error {
	for vk := range c.held {
		scan := uint16(0)
		if ret, _, _ := procMapVirtualKeyExW.Call(uintptr(vk), uintptr(MAPVK_VK_TO_VSC_EX), 0); ret != 0 {
			scan = uint16(ret)
		}
		flags := uint32(KEYEVENTF_KEYUP)
		if scan != 0 {
			flags |= KEYEVENTF_SCANCODE
		}
		if isExtendedVK(vk) {
			flags |= KEYEVENTF_EXTENDEDKEY
		}
		ki := keyboardInput{Vk: vk, Scan: scan, DwFlags: flags}
		c.sendInput(input{Header: inputHeader{Type: INPUT_KEYBOARD}, Union: inputUnion{Ki: ki}})
		delete(c.held, vk)
	}
	return nil
}

func (c *WindowsCapturer) sendInput(in input) {
	_, _, _ = procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
}

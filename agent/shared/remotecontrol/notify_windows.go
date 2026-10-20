//go:build windows

package remotecontrol

import (
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/windows"
)

var (
	procRegisterClassExW           = user32.NewProc("RegisterClassExW")
	procCreateWindowExW            = user32.NewProc("CreateWindowExW")
	procDestroyWindow              = user32.NewProc("DestroyWindow")
	procDefWindowProcW             = user32.NewProc("DefWindowProcW")
	procGetMessageW                = user32.NewProc("GetMessageW")
	procTranslateMessage           = user32.NewProc("TranslateMessage")
	procDispatchMessageW           = user32.NewProc("DispatchMessageW")
	procPostMessageW               = user32.NewProc("PostMessageW")
	procPostQuitMessage            = user32.NewProc("PostQuitMessage")
	procBeginPaint                 = user32.NewProc("BeginPaint")
	procEndPaint                   = user32.NewProc("EndPaint")
	procFillRect                   = user32.NewProc("FillRect")
	procDrawTextW                  = user32.NewProc("DrawTextW")
	procGetClientRect              = user32.NewProc("GetClientRect")
	procInvalidateRect             = user32.NewProc("InvalidateRect")
	procSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	procSetWindowDisplayAffinity   = user32.NewProc("SetWindowDisplayAffinity")

	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procCreateFontW      = gdi32.NewProc("CreateFontW")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
)

const (
	wsPopup           = 0x80000000
	wsVisible         = 0x10000000
	wsExTopmost       = 0x00000008
	wsExToolWindow    = 0x00000080
	wsExNoActivate    = 0x08000000
	wsExLayered       = 0x00080000
	wsExTransparent   = 0x00000020
	lwaAlpha          = 0x2
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmPaint           = 0x000F
	wmApp             = 0x8000
	wmBannerText      = wmApp + 1
	dtCenter          = 0x1
	dtVCenter         = 0x4
	dtSingleLine      = 0x20
	dtEndEllipsis     = 0x8000
	bkTransparent     = 1
	wdaExcludeCapture = 0x11

	bannerWidth  = 460
	bannerHeight = 30
)

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	PtX     int32
	PtY     int32
	_       uint32
}

type paintStruct struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     rect32
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

// winBanner is one banner window and the OS thread that owns it.
type winBanner struct {
	mu   sync.Mutex
	text string
	hwnd uintptr
}

var (
	bannerClassOnce sync.Once
	bannerClassName *uint16
	bannerClassErr  error
	bannerBrush     uintptr
	bannerFont      uintptr

	// bannersByHwnd lets the single window procedure find the text of the
	// window it is painting.
	bannersByHwnd sync.Map

	bannerWndProc = syscall.NewCallback(func(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
		switch msg {
		case wmPaint:
			paintBanner(hwnd)
			return 0
		case wmBannerText:
			procInvalidateRect.Call(hwnd, 0, 1)
			return 0
		case wmClose:
			procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			bannersByHwnd.Delete(hwnd)
			procPostQuitMessage.Call(0)
			return 0
		}
		r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return r
	})
)

func registerBannerClass() error {
	bannerClassOnce.Do(func() {
		bannerClassName, _ = windows.UTF16PtrFromString("EndpointAgentSessionBanner")
		// Dark slate with white text reads on any wallpaper and matches the
		// console's own remote viewport.
		bannerBrush, _, _ = procCreateSolidBrush.Call(0x003A2A1E) // COLORREF is 0x00BBGGRR
		face, _ := windows.UTF16PtrFromString("Segoe UI")
		bannerFont, _, _ = procCreateFontW.Call(uintptr(^uint32(15)), 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face)))
		wc := wndClassEx{
			LpfnWndProc:   bannerWndProc,
			HbrBackground: bannerBrush,
			LpszClassName: bannerClassName,
		}
		wc.CbSize = uint32(unsafe.Sizeof(wc))
		if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			bannerClassErr = err
		}
	})
	return bannerClassErr
}

func paintBanner(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	var rc rect32
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), bannerBrush)

	text := ""
	if v, ok := bannersByHwnd.Load(hwnd); ok {
		b := v.(*winBanner)
		b.mu.Lock()
		text = b.text
		b.mu.Unlock()
	}
	u, err := windows.UTF16FromString(text)
	if err != nil {
		return
	}
	if bannerFont != 0 {
		old, _, _ := procSelectObject.Call(hdc, bannerFont)
		defer procSelectObject.Call(hdc, old)
	}
	procSetBkMode.Call(hdc, bkTransparent)
	procSetTextColor.Call(hdc, 0x00FFFFFF)
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&rc)),
		dtCenter|dtVCenter|dtSingleLine|dtEndEllipsis)
}

// showSessionBanner opens the banner at the top centre of the primary monitor.
//
// It runs on a goroutine of its own, locked to one OS thread, because a window
// belongs to the thread that created it and only that thread receives its
// messages. It must never be the capture thread either: SetThreadDesktop refuses
// a thread that owns a window, so a banner created there would stop the capture
// from following a desktop switch.
//
// A failure to show the banner is logged and the session carries on: the banner
// is a courtesy to the endpoint user, and refusing to support them because a
// window could not be created would help nobody.
func showSessionBanner(text string) sessionBanner {
	b := &winBanner{text: text}
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_ = attachToInputDesktop()

		created := func() bool {
			defer close(ready)
			if err := registerBannerClass(); err != nil {
				log.Warn().Err(err).Msg("remote control banner: register window class")
				return false
			}
			screenW, _, _ := procGetSystemMetrics.Call(uintptr(SM_CXSCREEN))
			x := (int(screenW) - bannerWidth) / 2
			title, _ := windows.UTF16PtrFromString("Remote session")
			// Click-through (layered + transparent) and never activated: the
			// banner must not take focus from, or eat clicks meant for, the
			// window the operator or the user is working in.
			hwnd, _, err := procCreateWindowExW.Call(
				wsExTopmost|wsExToolWindow|wsExNoActivate|wsExLayered|wsExTransparent,
				uintptr(unsafe.Pointer(bannerClassName)),
				uintptr(unsafe.Pointer(title)),
				wsPopup|wsVisible,
				uintptr(x), 0, bannerWidth, bannerHeight,
				0, 0, 0, 0,
			)
			if hwnd == 0 {
				log.Warn().Err(err).Msg("remote control banner: create window")
				return false
			}
			b.mu.Lock()
			b.hwnd = hwnd
			b.mu.Unlock()
			bannersByHwnd.Store(hwnd, b)
			procSetLayeredWindowAttributes.Call(hwnd, 0, 235, lwaAlpha)
			// Kept out of the operator's frames (Windows 10 2004 and later): the
			// banner is for the person in front of the screen, and in the
			// stream it would only cover the top of the desktop. Older builds
			// refuse the flag and show it in the frames too, which is harmless.
			procSetWindowDisplayAffinity.Call(hwnd, wdaExcludeCapture)
			procInvalidateRect.Call(hwnd, 0, 1)
			return true
		}()
		if !created {
			return
		}

		var m winMsg
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				return
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	<-ready
	return b
}

func (b *winBanner) SetText(text string) {
	b.mu.Lock()
	b.text = text
	hwnd := b.hwnd
	b.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, wmBannerText, 0, 0)
	}
}

// Close posts rather than sends: Stop must not block on a window thread that
// is, for whatever reason, not pumping messages.
func (b *winBanner) Close() {
	b.mu.Lock()
	hwnd := b.hwnd
	b.hwnd = 0
	b.mu.Unlock()
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}
}

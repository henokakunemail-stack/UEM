//go:build windows

package remotecontrol

import (
	"fmt"
	"sort"
	"sync"
	"syscall"
	"unsafe"
)

var (
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procGetCursorInfo       = user32.NewProc("GetCursorInfo")
)

const (
	SM_XVIRTUALSCREEN  = 76
	SM_YVIRTUALSCREEN  = 77
	SM_CXVIRTUALSCREEN = 78
	SM_CYVIRTUALSCREEN = 79

	monitorInfoPrimary = 0x1
	cursorShowing      = 0x1

	// monitorPrimary is the capturer's default selection. It is not an index:
	// the primary monitor is resolved on every frame, so a session that started
	// before the user changed their primary display follows the change.
	monitorPrimary = -2
	// monitorAll is the whole virtual desktop, every monitor in one frame.
	monitorAll = -1
)

type rect32 struct {
	Left, Top, Right, Bottom int32
}

type monitorInfo struct {
	CbSize    uint32
	RcMonitor rect32
	RcWork    rect32
	DwFlags   uint32
}

type cursorInfo struct {
	CbSize  uint32
	Flags   uint32
	HCursor uintptr
	X, Y    int32
}

// EnumDisplayMonitors reports through a callback, and syscall.NewCallback slots
// are a fixed, never-freed resource, so there is exactly one callback for the
// process and enumMu serialises the enumerations that share its output slice.
var (
	enumMu  sync.Mutex
	enumOut []MonitorInfo

	enumCallback = syscall.NewCallback(func(hMonitor, hdc, lprc, lparam uintptr) uintptr {
		var mi monitorInfo
		mi.CbSize = uint32(unsafe.Sizeof(mi))
		if r, _, _ := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&mi))); r != 0 {
			enumOut = append(enumOut, MonitorInfo{
				X:       int(mi.RcMonitor.Left),
				Y:       int(mi.RcMonitor.Top),
				Width:   int(mi.RcMonitor.Right - mi.RcMonitor.Left),
				Height:  int(mi.RcMonitor.Bottom - mi.RcMonitor.Top),
				Primary: mi.DwFlags&monitorInfoPrimary != 0,
			})
		}
		return 1 // continue enumerating
	})
)

// enumMonitors lists the attached monitors, ordered left to right and then top
// to bottom. Windows enumerates in an order of its own that can change when a
// display is reconnected; ordering by position keeps "monitor 2" the one on the
// right for as long as it physically is.
func enumMonitors() []MonitorInfo {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumOut = nil
	procEnumDisplayMonitors.Call(0, 0, enumCallback, 0)
	out := enumOut
	enumOut = nil
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].X != out[j].X {
			return out[i].X < out[j].X
		}
		return out[i].Y < out[j].Y
	})
	for i := range out {
		out[i].Index = i
	}
	return out
}

func virtualScreen() MonitorInfo {
	x, _, _ := procGetSystemMetrics.Call(SM_XVIRTUALSCREEN)
	y, _, _ := procGetSystemMetrics.Call(SM_YVIRTUALSCREEN)
	w, _, _ := procGetSystemMetrics.Call(SM_CXVIRTUALSCREEN)
	h, _, _ := procGetSystemMetrics.Call(SM_CYVIRTUALSCREEN)
	// The virtual origin is negative when a monitor sits left of or above the
	// primary, and GetSystemMetrics returns it as an int in a uintptr.
	return MonitorInfo{Index: monitorAll, X: int(int32(x)), Y: int(int32(y)), Width: int(int32(w)), Height: int(int32(h))}
}

func primaryScreen() MonitorInfo {
	w, _, _ := procGetSystemMetrics.Call(uintptr(SM_CXSCREEN))
	h, _, _ := procGetSystemMetrics.Call(uintptr(SM_CYSCREEN))
	return MonitorInfo{Index: monitorPrimary, Width: int(w), Height: int(h), Primary: true}
}

// Monitors implements monitorSelector.
func (c *WindowsCapturer) Monitors() []MonitorInfo {
	_ = attachToInputDesktop()
	return enumMonitors()
}

// SelectMonitor implements monitorSelector. The next frame is captured from the
// chosen monitor; the session forces a keyframe so the console repaints whole.
func (c *WindowsCapturer) SelectMonitor(index int) (MonitorInfo, error) {
	_ = attachToInputDesktop()
	var chosen MonitorInfo
	switch {
	case index == monitorAll:
		chosen = virtualScreen()
	default:
		mons := enumMonitors()
		if index < 0 || index >= len(mons) {
			return MonitorInfo{}, fmt.Errorf("monitor %d does not exist (this endpoint has %d)", index, len(mons))
		}
		chosen = mons[index]
	}
	c.geoMu.Lock()
	c.selected = index
	c.geoMu.Unlock()
	return chosen, nil
}

// captureRect resolves the current selection to screen coordinates and records
// its origin for input mapping. It is resolved per frame rather than stored, so
// a monitor that is unplugged mid-session falls back to the primary instead of
// blitting a rectangle that no longer exists.
func (c *WindowsCapturer) captureRect() MonitorInfo {
	c.geoMu.Lock()
	sel := c.selected
	c.geoMu.Unlock()

	r := primaryScreen()
	switch {
	case sel == monitorAll:
		r = virtualScreen()
	case sel >= 0:
		if mons := enumMonitors(); sel < len(mons) {
			r = mons[sel]
		}
	}

	c.geoMu.Lock()
	c.originX, c.originY = r.X, r.Y
	c.areaW, c.areaH = r.Width, r.Height
	c.geoMu.Unlock()
	return r
}

// origin is where the captured area starts on the virtual desktop. Console
// coordinates are relative to the frame, so input is shifted by it.
func (c *WindowsCapturer) origin() (int, int) {
	c.geoMu.Lock()
	defer c.geoMu.Unlock()
	return c.originX, c.originY
}

// CursorPosition implements cursorReporter: the pointer in frame coordinates,
// and whether it is shown and inside the captured area. BitBlt does not draw
// the cursor, so without this a View-mode operator cannot see where the person
// at the endpoint is pointing.
func (c *WindowsCapturer) CursorPosition() (int, int, bool) {
	var ci cursorInfo
	ci.CbSize = uint32(unsafe.Sizeof(ci))
	if r, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); r == 0 {
		return 0, 0, false
	}
	c.geoMu.Lock()
	ox, oy, w, h := c.originX, c.originY, c.areaW, c.areaH
	c.geoMu.Unlock()
	x, y := int(ci.X)-ox, int(ci.Y)-oy
	visible := ci.Flags&cursorShowing != 0 && x >= 0 && y >= 0 && x < w && y < h
	return x, y, visible
}

//go:build windows

package remotecontrol

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Desktop Duplication (DXGI 1.2, Windows 8 and later) hands the compositor's
// own copy of a monitor to the caller through the GPU. GDI's BitBlt has to ask
// the compositor to read the composed desktop back into system memory on every
// call, which measured 50-80ms for one 1080p frame and was the largest single
// cost of a remote session once encoding ran on every core. Duplication copies
// the same frame in a few milliseconds, and reports when nothing has changed,
// so a still desktop costs no copy at all.
//
// It is called through COM vtables rather than through a binding package so the
// agent stays a CGO-free build with no new dependency. Only the methods used are
// declared; their slot numbers are fixed by the published interface layouts.

var (
	modD3D11 = syscall.NewLazyDLL("d3d11.dll")
	modDXGI  = syscall.NewLazyDLL("dxgi.dll")

	procD3D11CreateDevice  = modD3D11.NewProc("D3D11CreateDevice")
	procCreateDXGIFactory1 = modDXGI.NewProc("CreateDXGIFactory1")
)

var (
	iidIDXGIFactory1   = windows.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	iidIDXGIOutput1    = windows.GUID{Data1: 0x00cddea8, Data2: 0x939b, Data3: 0x4b83, Data4: [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
	iidID3D11Texture2D = windows.GUID{Data1: 0x6f15aaf2, Data2: 0xd208, Data3: 0x4e89, Data4: [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
)

// Vtable slots. IUnknown takes 0-2 and IDXGIObject 3-6 on every DXGI
// interface; ID3D11DeviceChild takes 3-6 on the device context and textures.
const (
	slotQueryInterface = 0
	slotRelease        = 2

	slotFactoryEnumAdapters1 = 12 // IDXGIFactory1
	slotAdapterEnumOutputs   = 7  // IDXGIAdapter
	slotOutputGetDesc        = 7  // IDXGIOutput
	slotOutputDuplicate      = 22 // IDXGIOutput1::DuplicateOutput

	slotDuplAcquireNextFrame = 8  // IDXGIOutputDuplication
	slotDuplReleaseFrame     = 14 // IDXGIOutputDuplication

	slotDeviceCreateTexture2D = 5  // ID3D11Device
	slotContextMap            = 14 // ID3D11DeviceContext
	slotContextUnmap          = 15
	slotContextCopyResource   = 47
	slotTexture2DGetDesc      = 10 // ID3D11Texture2D
)

const (
	dxgiErrorNotFound    = 0x887A0002
	dxgiErrorAccessLost  = 0x887A0026
	dxgiErrorWaitTimeout = 0x887A0027

	d3dDriverTypeUnknown = 0
	d3d11SDKVersion      = 7

	dxgiFormatB8G8R8A8   = 87
	d3d11UsageStaging    = 3
	d3d11CPUAccessRead   = 0x20000
	d3d11MapRead         = 1
	dxgiModeRotationNone = 1
)

// errNoNewFrame means the desktop has not changed since the last frame and no
// earlier frame is held to repeat.
var errNoNewFrame = errors.New("desktop duplication has no frame yet")

// errDuplicationLost is a desktop switch (UAC, Ctrl+Alt+Del, the lock screen) or
// a mode change: the duplication is dead and has to be created again.
var errDuplicationLost = errors.New("desktop duplication access lost")

type hresult uint32

func (h hresult) failed() bool { return int32(h) < 0 }

func hrErr(op string, h hresult) error {
	if h == dxgiErrorAccessLost {
		return fmt.Errorf("%s: %w", op, errDuplicationLost)
	}
	return fmt.Errorf("%s failed: HRESULT 0x%08X", op, uint32(h))
}

// vmethod is the address of slot `method` in the vtable of the COM interface
// at obj.
func vmethod(obj unsafe.Pointer, method int) uintptr {
	return (*(**[64]uintptr)(obj))[method]
}

// comCall calls a COM method whose arguments hold no address of Go memory:
// integers, and pointers to COM objects, which Windows owns. A call that passes
// the address of a Go variable is written out with syscall.SyscallN instead;
// see the note above newDXGIDuplicator.
//
// None of those calls has a result worth checking: Release returns a count,
// CopyResource, Unmap and ReleaseFrame return nothing or nothing actionable.
func comCall(obj unsafe.Pointer, method int, args ...uintptr) {
	_, _, _ = syscall.SyscallN(vmethod(obj, method), append([]uintptr{uintptr(obj)}, args...)...)
}

func comRelease(obj unsafe.Pointer) {
	if obj != nil {
		comCall(obj, slotRelease)
	}
}

// dxgiOutputDesc is DXGI_OUTPUT_DESC.
type dxgiOutputDesc struct {
	DeviceName         [32]uint16
	DesktopCoordinates rect32
	AttachedToDesktop  int32
	Rotation           uint32
	Monitor            uintptr
}

// dxgiFrameInfo is DXGI_OUTDUPL_FRAME_INFO.
type dxgiFrameInfo struct {
	LastPresentTime           int64
	LastMouseUpdateTime       int64
	AccumulatedFrames         uint32
	RectsCoalesced            int32
	ProtectedContentMaskedOut int32
	PointerX, PointerY        int32
	PointerVisible            int32
	TotalMetadataBufferSize   uint32
	PointerShapeBufferSize    uint32
}

// d3d11Texture2DDesc is D3D11_TEXTURE2D_DESC.
type d3d11Texture2DDesc struct {
	Width, Height  uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleCount    uint32
	SampleQuality  uint32
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

// d3d11MappedSubresource is D3D11_MAPPED_SUBRESOURCE.
type d3d11MappedSubresource struct {
	Data       unsafe.Pointer
	RowPitch   uint32
	DepthPitch uint32
}

// dxgiDuplicator duplicates one monitor. It is used from the capture thread
// only: the D3D11 immediate context is not safe to share between threads.
type dxgiDuplicator struct {
	area    MonitorInfo
	device  unsafe.Pointer
	context unsafe.Pointer
	dupl    unsafe.Pointer
	staging unsafe.Pointer
	// last is the most recent frame, returned again while the desktop is
	// unchanged. It is never written after it is returned.
	last []byte
}

// A note on every call below that hands Windows the address of a Go variable:
// the conversion to uintptr is written in the argument list of the
// syscall.SyscallN or LazyProc.Call itself. The compiler keeps memory named
// that way in place for the duration of the call. Converting first and passing
// the uintptr through a helper does not: the helper's prologue may grow the
// goroutine's stack, Go then moves the stack, and Windows writes the result to
// where the variable used to be. That was seen as a Map that reported success
// and left the mapped pointer nil, about one session in four.

// newDXGIDuplicator finds the DXGI output that is exactly the area being
// captured and duplicates it. Anything it cannot capture faithfully -- an area
// that spans several monitors, a rotated monitor, a session with no duplication
// support (RDP, a basic display driver), or the secure desktop -- is an error,
// and the capture uses GDI instead.
func newDXGIDuplicator(area MonitorInfo) (*dxgiDuplicator, error) {
	if err := procCreateDXGIFactory1.Find(); err != nil {
		return nil, fmt.Errorf("dxgi unavailable: %w", err)
	}
	var factory unsafe.Pointer
	if r, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); hresult(r).failed() {
		return nil, hrErr("CreateDXGIFactory1", hresult(r))
	}
	defer comRelease(factory)

	for ai := 0; ; ai++ {
		var adapter unsafe.Pointer
		r, _, _ := syscall.SyscallN(vmethod(factory, slotFactoryEnumAdapters1), uintptr(factory), uintptr(ai), uintptr(unsafe.Pointer(&adapter)))
		h := hresult(r)
		if h == dxgiErrorNotFound {
			break
		}
		if h.failed() {
			return nil, hrErr("EnumAdapters1", h)
		}
		d, found, err := duplicateOnAdapter(adapter, area)
		comRelease(adapter)
		if found {
			return d, err
		}
	}
	return nil, fmt.Errorf("no single DXGI output covers %dx%d at (%d,%d)", area.Width, area.Height, area.X, area.Y)
}

// duplicateOnAdapter looks for the area among one adapter's outputs. found is
// true once the matching output is seen, whether or not duplicating it worked.
func duplicateOnAdapter(adapter unsafe.Pointer, area MonitorInfo) (*dxgiDuplicator, bool, error) {
	for oi := 0; ; oi++ {
		var output unsafe.Pointer
		r, _, _ := syscall.SyscallN(vmethod(adapter, slotAdapterEnumOutputs), uintptr(adapter), uintptr(oi), uintptr(unsafe.Pointer(&output)))
		h := hresult(r)
		if h == dxgiErrorNotFound {
			return nil, false, nil
		}
		if h.failed() {
			return nil, false, hrErr("EnumOutputs", h)
		}
		var desc dxgiOutputDesc
		r, _, _ = syscall.SyscallN(vmethod(output, slotOutputGetDesc), uintptr(output), uintptr(unsafe.Pointer(&desc)))
		rc := desc.DesktopCoordinates
		match := !hresult(r).failed() && desc.AttachedToDesktop != 0 &&
			int(rc.Left) == area.X && int(rc.Top) == area.Y &&
			int(rc.Right-rc.Left) == area.Width && int(rc.Bottom-rc.Top) == area.Height
		if !match {
			comRelease(output)
			continue
		}
		defer comRelease(output)
		if desc.Rotation != dxgiModeRotationNone && desc.Rotation != 0 {
			return nil, true, fmt.Errorf("monitor is rotated (DXGI rotation %d)", desc.Rotation)
		}
		d, err := duplicateOutput(adapter, output, area)
		return d, true, err
	}
}

func duplicateOutput(adapter, output unsafe.Pointer, area MonitorInfo) (*dxgiDuplicator, error) {
	d := &dxgiDuplicator{area: area}
	var device, context unsafe.Pointer
	var featureLevel uint32
	r, _, _ := procD3D11CreateDevice.Call(
		uintptr(adapter), d3dDriverTypeUnknown, 0, 0, 0, 0, d3d11SDKVersion,
		uintptr(unsafe.Pointer(&device)), uintptr(unsafe.Pointer(&featureLevel)), uintptr(unsafe.Pointer(&context)))
	if hresult(r).failed() {
		return nil, hrErr("D3D11CreateDevice", hresult(r))
	}
	d.device, d.context = device, context

	var output1 unsafe.Pointer
	r, _, _ = syscall.SyscallN(vmethod(output, slotQueryInterface), uintptr(output), uintptr(unsafe.Pointer(&iidIDXGIOutput1)), uintptr(unsafe.Pointer(&output1)))
	if hresult(r).failed() {
		d.close()
		return nil, hrErr("QueryInterface(IDXGIOutput1)", hresult(r))
	}
	defer comRelease(output1)

	var dupl unsafe.Pointer
	r, _, _ = syscall.SyscallN(vmethod(output1, slotOutputDuplicate), uintptr(output1), uintptr(d.device), uintptr(unsafe.Pointer(&dupl)))
	if hresult(r).failed() {
		d.close()
		return nil, hrErr("DuplicateOutput", hresult(r))
	}
	d.dupl = dupl
	return d, nil
}

func (d *dxgiDuplicator) matches(area MonitorInfo) bool {
	return area.X == d.area.X && area.Y == d.area.Y && area.Width == d.area.Width && area.Height == d.area.Height
}

// capture returns the monitor's current image as BGRA rows with no padding.
// While the desktop is unchanged it returns the previous frame again.
func (d *dxgiDuplicator) capture() ([]byte, error) {
	// The first acquire waits briefly for the image the duplication starts
	// with; after that the call only asks whether a new frame exists.
	timeout := uintptr(0)
	if d.last == nil {
		timeout = 100
	}
	var info dxgiFrameInfo
	var resource unsafe.Pointer
	r, _, _ := syscall.SyscallN(vmethod(d.dupl, slotDuplAcquireNextFrame), uintptr(d.dupl), timeout,
		uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&resource)))
	h := hresult(r)
	if h == dxgiErrorWaitTimeout {
		if d.last == nil {
			return nil, errNoNewFrame
		}
		return d.last, nil
	}
	if h.failed() {
		return nil, hrErr("AcquireNextFrame", h)
	}
	defer comCall(d.dupl, slotDuplReleaseFrame)
	defer comRelease(resource)

	// Only the pointer moved: the image is the one already held.
	if info.LastPresentTime == 0 && d.last != nil {
		return d.last, nil
	}

	var tex unsafe.Pointer
	r, _, _ = syscall.SyscallN(vmethod(resource, slotQueryInterface), uintptr(resource), uintptr(unsafe.Pointer(&iidID3D11Texture2D)), uintptr(unsafe.Pointer(&tex)))
	if hresult(r).failed() {
		return nil, hrErr("QueryInterface(ID3D11Texture2D)", hresult(r))
	}
	defer comRelease(tex)

	if d.staging == nil {
		var desc d3d11Texture2DDesc
		_, _, _ = syscall.SyscallN(vmethod(tex, slotTexture2DGetDesc), uintptr(tex), uintptr(unsafe.Pointer(&desc)))
		if int(desc.Width) != d.area.Width || int(desc.Height) != d.area.Height || desc.Format != dxgiFormatB8G8R8A8 {
			return nil, fmt.Errorf("duplicated surface is %dx%d format %d, the monitor is %dx%d",
				desc.Width, desc.Height, desc.Format, d.area.Width, d.area.Height)
		}
		desc.MipLevels, desc.ArraySize = 1, 1
		desc.SampleCount, desc.SampleQuality = 1, 0
		desc.Usage = d3d11UsageStaging
		desc.BindFlags = 0
		desc.CPUAccessFlags = d3d11CPUAccessRead
		desc.MiscFlags = 0
		var staging unsafe.Pointer
		r, _, _ = syscall.SyscallN(vmethod(d.device, slotDeviceCreateTexture2D), uintptr(d.device),
			uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&staging)))
		if hresult(r).failed() {
			return nil, hrErr("CreateTexture2D(staging)", hresult(r))
		}
		d.staging = staging
	}

	comCall(d.context, slotContextCopyResource, uintptr(d.staging), uintptr(tex))
	var mapped d3d11MappedSubresource
	r, _, _ = syscall.SyscallN(vmethod(d.context, slotContextMap), uintptr(d.context), uintptr(d.staging), 0, d3d11MapRead, 0,
		uintptr(unsafe.Pointer(&mapped)))
	if hresult(r).failed() {
		return nil, hrErr("Map(staging)", hresult(r))
	}
	if mapped.Data == nil || int(mapped.RowPitch) < d.area.Width*4 {
		comCall(d.context, slotContextUnmap, uintptr(d.staging), 0)
		return nil, fmt.Errorf("Map(staging) returned no usable surface (pitch %d)", mapped.RowPitch)
	}
	w, ht, pitch := d.area.Width, d.area.Height, int(mapped.RowPitch)
	src := unsafe.Slice((*byte)(mapped.Data), pitch*(ht-1)+w*4)
	px := make([]byte, w*ht*4)
	for y := 0; y < ht; y++ {
		copy(px[y*w*4:(y+1)*w*4], src[y*pitch:y*pitch+w*4])
	}
	comCall(d.context, slotContextUnmap, uintptr(d.staging), 0)
	d.last = px
	return px, nil
}

func (d *dxgiDuplicator) close() {
	comRelease(d.staging)
	comRelease(d.dupl)
	comRelease(d.context)
	comRelease(d.device)
	d.staging, d.dupl, d.context, d.device = nil, nil, nil, nil
	d.last = nil
}

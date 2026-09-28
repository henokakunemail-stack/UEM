//go:build darwin

package remotecontrol

import "fmt"

// DarwinCapturer reports itself as incapable rather than emitting a placeholder.
// See the note on LinuxCapturer: the previous build returned a solid-grey JPEG
// and a no-op injector, which made the console look live while nothing was.
//
// ponytail: no CGDisplay streaming and no CGEvent posting. Both need cgo
// (CoreGraphics), which this CGO-free build does not take on. Add them behind a
// cgo build tag; macOS is not a supported remote-control target until then.
type DarwinCapturer struct{}

func NewPlatformCapturer() ScreenCapturer {
	return &DarwinCapturer{}
}

func (c *DarwinCapturer) Capabilities() Capabilities {
	return Capabilities{
		Capture:  false,
		Mouse:    false,
		Keyboard: false,
		Reason:   "Screen capture and input injection are not implemented for macOS. This build is CGO-free and has no CoreGraphics backend.",
	}
}

func (c *DarwinCapturer) CaptureScreen() ([]byte, int, int, error) {
	return nil, 0, 0, fmt.Errorf("screen capture is not implemented on macOS")
}

func (c *DarwinCapturer) InjectMouseEvent(e InputEvent) error {
	return fmt.Errorf("input injection is not implemented on macOS")
}

func (c *DarwinCapturer) InjectKeyboardEvent(e InputEvent) error {
	return fmt.Errorf("input injection is not implemented on macOS")
}

func (c *DarwinCapturer) ReleaseAllKeys() error { return nil }

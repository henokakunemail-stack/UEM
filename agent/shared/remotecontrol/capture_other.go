//go:build !windows && !linux && !darwin

package remotecontrol

import (
	"fmt"
	"runtime"
)

// OtherCapturer is the fallback for any other GOOS (freebsd, openbsd, js/wasm).
// It reports itself as incapable; see the note on LinuxCapturer for why a
// placeholder image is not an acceptable substitute.
type OtherCapturer struct{}

func NewPlatformCapturer() ScreenCapturer {
	return &OtherCapturer{}
}

func (c *OtherCapturer) Capabilities() Capabilities {
	return Capabilities{
		Capture:  false,
		Mouse:    false,
		Keyboard: false,
		Reason:   fmt.Sprintf("Screen capture and input injection are not implemented for %s.", runtime.GOOS),
	}
}

func (c *OtherCapturer) CaptureScreen() ([]byte, int, int, error) {
	return nil, 0, 0, fmt.Errorf("screen capture is not implemented on this platform")
}

func (c *OtherCapturer) InjectMouseEvent(e InputEvent) error {
	return fmt.Errorf("input injection is not implemented on this platform")
}

func (c *OtherCapturer) InjectKeyboardEvent(e InputEvent) error {
	return fmt.Errorf("input injection is not implemented on this platform")
}

func (c *OtherCapturer) ReleaseAllKeys() error { return nil }

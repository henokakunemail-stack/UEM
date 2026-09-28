//go:build linux

package remotecontrol

import "fmt"

// LinuxCapturer reports itself as incapable rather than emitting a placeholder.
//
// The previous implementation returned a solid-grey JPEG of a fixed 1280x720 and
// a no-op input injector. That is worse than no feature: the console painted a
// believable "live desktop", the toolbar showed a frame rate, and the operator
// clicked and typed into a void with nothing anywhere reporting a problem. A
// session that cannot work should say so on the first frame.
//
// Framebuffer capture on Linux needs X11 or Wayland libraries that pull in CGO
// (x11 via XGetImage, Wayland via wlr-screencopy), which this agent — built
// CGO-free for a single static binary — deliberately does not take on. Until
// that exists, the session starts, says it cannot capture, and the console
// shows the reason.
//
// ponytail: no X11/Wayland capture and no uinput injection. Add
// golang.org/x/display (needs cgo) or a wlr-screencopy client, and /dev/uinput
// writes, behind build tags so the CGO-free build keeps working.
type LinuxCapturer struct{}

func NewPlatformCapturer() ScreenCapturer {
	return &LinuxCapturer{}
}

func (c *LinuxCapturer) Capabilities() Capabilities {
	return Capabilities{
		Capture:  false,
		Mouse:    false,
		Keyboard: false,
		Reason:   "Screen capture and input injection are not implemented for Linux. This build is CGO-free and has no X11 or Wayland framebuffer backend.",
	}
}

func (c *LinuxCapturer) CaptureScreen() ([]byte, int, int, error) {
	return nil, 0, 0, fmt.Errorf("screen capture is not implemented on Linux")
}

func (c *LinuxCapturer) InjectMouseEvent(e InputEvent) error {
	return fmt.Errorf("input injection is not implemented on Linux")
}

func (c *LinuxCapturer) InjectKeyboardEvent(e InputEvent) error {
	return fmt.Errorf("input injection is not implemented on Linux")
}

func (c *LinuxCapturer) ReleaseAllKeys() error { return nil }

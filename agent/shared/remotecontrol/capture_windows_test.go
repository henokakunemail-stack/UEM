//go:build windows

package remotecontrol

import (
	"testing"
	"unsafe"
)

// The capability probe and the real capture have to agree. They used to disagree
// in the only direction that matters: the probe ran on a thread with no desktop
// and reported the host as unable to capture, while the real capture would have
// been equally unable -- so the console was right to refuse, but for a reason
// that was never stated. What has to hold now is that once the thread is
// attached, both succeed, and neither claims a capability it cannot deliver.
func TestTheProbeAndTheCaptureAgreeOnThisHost(t *testing.T) {
	c := NewPlatformCapturer()

	caps := c.Capabilities()
	data, w, h, err := c.CaptureScreen()

	if caps.Capture != (err == nil) {
		t.Fatalf("probe says capture=%v but CaptureScreen returned err=%v (reason: %s)",
			caps.Capture, err, caps.Reason)
	}
	if err != nil {
		t.Skipf("this host cannot be captured, which the probe already said: %v", err)
	}

	if w <= 0 || h <= 0 {
		t.Fatalf("capture reported %dx%d", w, h)
	}
	if len(data) == 0 {
		t.Fatal("capture succeeded with no bytes")
	}
}

// A capture is raw pixels now, not an encoded image, and the length is the
// whole contract. Diffing has to happen before any encoding -- a JPEG of a
// desktop that changed in one corner differs across the whole image, because
// the encoder's block boundaries moved, so an encoded frame is useless as a
// diff baseline. A short buffer here is a rect that lands in the wrong place on
// the console, and an over-long one is memory nobody asked for.
func TestACapturedFrameIsRawPixelsSizedToTheDesktop(t *testing.T) {
	c := NewPlatformCapturer()
	if caps := c.Capabilities(); !caps.Capture {
		t.Skipf("this host cannot be captured: %s", caps.Reason)
	}

	data, w, h, err := c.CaptureScreen()
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}
	if want := w * h * 4; len(data) != want {
		t.Fatalf("capture returned %d bytes for a %dx%d desktop, want %d", len(data), w, h, want)
	}
}

// The desktop attach is per thread, so the streaming goroutine and the goroutine
// that built the capturer are different threads and both have to be able to
// capture. This is the shape of the defect: attaching only the constructor's
// thread left the capture goroutine exactly as it was.
func TestACapturerWorksFromMoreThanOneThread(t *testing.T) {
	if caps := NewPlatformCapturer().Capabilities(); !caps.Capture {
		t.Skipf("this host cannot be captured: %s", caps.Reason)
	}

	c := NewPlatformCapturer()
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, _, _, err := c.CaptureScreen()
			results <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-results; err != nil {
			t.Fatalf("capture from a second goroutine failed: %v", err)
		}
	}
}

// WindowsCapturer is shared between the capability probe, the capture loop and
// the input paths, and they run on different goroutines. A concurrent map write
// on the held-key set is a crash that only shows up under load, in production,
// on the machine being administered.
func TestKeyboardTrackingIsSafeUnderConcurrency(t *testing.T) {
	c := NewPlatformCapturer()
	if caps := c.Capabilities(); !caps.Capture {
		t.Skipf("this host cannot be captured: %s", caps.Reason)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = c.InjectKeyboardEvent(InputEvent{Type: "keyboard", Action: "down", Code: 0x41})
			_ = c.InjectKeyboardEvent(InputEvent{Type: "keyboard", Action: "up", Code: 0x41})
		}
	}()

	for i := 0; i < 20; i++ {
		if _, _, _, err := c.CaptureScreen(); err != nil {
			t.Fatalf("CaptureScreen: %v", err)
		}
	}
	<-done
	_ = c.ReleaseAllKeys()
}

func TestWin32InputStructContract(t *testing.T) {
	// Win32 tagINPUT on 64-bit Windows must be exactly 40 bytes.
	// Passing an incorrect cbSize to SendInput causes the syscall to return 0 with ERROR_INVALID_PARAMETER.
	if sz := unsafe.Sizeof(win32Input{}); sz != 40 {
		t.Fatalf("win32Input size = %d bytes, want exactly 40 bytes for Win32 tagINPUT", sz)
	}
	if sz := unsafe.Sizeof(mouseInput{}); sz != 32 {
		t.Fatalf("mouseInput size = %d bytes, want exactly 32 bytes for Win32 MOUSEINPUT", sz)
	}
	if sz := unsafe.Sizeof(keyboardInput{}); sz != 32 {
		t.Fatalf("keyboardInput size = %d bytes, want exactly 32 bytes for Win32 KEYBDINPUT union slot", sz)
	}
}

func TestInjectMouseEventSucceeds(t *testing.T) {
	c := NewPlatformCapturer()
	if caps := c.Capabilities(); !caps.Capture {
		t.Skipf("this host cannot be captured/injected: %s", caps.Reason)
	}

	// Move mouse
	if err := c.InjectMouseEvent(InputEvent{Type: "mouse", Action: "move", X: 100, Y: 100}); err != nil {
		t.Fatalf("InjectMouseEvent move failed: %v", err)
	}

	// Left click down and up
	if err := c.InjectMouseEvent(InputEvent{Type: "mouse", Action: "down", X: 100, Y: 100, Button: "left"}); err != nil {
		t.Fatalf("InjectMouseEvent left down failed: %v", err)
	}
	if err := c.InjectMouseEvent(InputEvent{Type: "mouse", Action: "up", X: 100, Y: 100, Button: "left"}); err != nil {
		t.Fatalf("InjectMouseEvent left up failed: %v", err)
	}

	// Right click down and up
	if err := c.InjectMouseEvent(InputEvent{Type: "mouse", Action: "down", X: 100, Y: 100, Button: "right"}); err != nil {
		t.Fatalf("InjectMouseEvent right down failed: %v", err)
	}
	if err := c.InjectMouseEvent(InputEvent{Type: "mouse", Action: "up", X: 100, Y: 100, Button: "right"}); err != nil {
		t.Fatalf("InjectMouseEvent right up failed: %v", err)
	}

	// Wheel scroll
	if err := c.InjectMouseEvent(InputEvent{Type: "mouse", Action: "wheel", X: 100, Y: 100, Delta: 120}); err != nil {
		t.Fatalf("InjectMouseEvent wheel failed: %v", err)
	}
}

func TestInjectKeyboardEventSucceeds(t *testing.T) {
	c := NewPlatformCapturer()
	if caps := c.Capabilities(); !caps.Capture {
		t.Skipf("this host cannot be captured/injected: %s", caps.Reason)
	}

	// Virtual-key code 0x11 (VK_CONTROL)
	if err := c.InjectKeyboardEvent(InputEvent{Type: "keyboard", Action: "down", Code: 0x11}); err != nil {
		t.Fatalf("InjectKeyboardEvent down failed: %v", err)
	}
	if err := c.InjectKeyboardEvent(InputEvent{Type: "keyboard", Action: "up", Code: 0x11}); err != nil {
		t.Fatalf("InjectKeyboardEvent up failed: %v", err)
	}
	_ = c.ReleaseAllKeys()
}

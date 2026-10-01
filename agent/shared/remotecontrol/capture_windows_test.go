//go:build windows

package remotecontrol

import (
	"bytes"
	"image/jpeg"
	"testing"
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

// A frame that is not a decodable JPEG is not a frame. The console hands the
// payload straight to createImageBitmap as image/jpeg, so anything that is not
// a real JPEG is a blank canvas with a nonzero byte count behind it -- exactly
// the failure the operator cannot tell from a black desktop.
func TestACapturedFrameIsARealJPEG(t *testing.T) {
	c := NewPlatformCapturer()
	if caps := c.Capabilities(); !caps.Capture {
		t.Skipf("this host cannot be captured: %s", caps.Reason)
	}

	data, _, _, err := c.CaptureScreen()
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}

	if !bytes.HasPrefix(data, []byte{0xFF, 0xD8}) {
		t.Fatalf("frame does not start with the JPEG SOI marker: % X", data[:min(4, len(data))])
	}
	if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("frame is not a decodable JPEG: %v", err)
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

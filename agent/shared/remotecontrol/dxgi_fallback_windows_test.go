//go:build windows

package remotecontrol

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeSource stands in for desktop duplication: it returns a recognisable
// frame, or the error the test scripts.
type fakeSource struct {
	area   MonitorInfo
	err    error
	closed bool
	frames int
}

func (f *fakeSource) capture() ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.frames++
	px := make([]byte, f.area.Width*f.area.Height*4)
	for i := range px {
		px[i] = 0xAB
	}
	return px, nil
}
func (f *fakeSource) matches(a MonitorInfo) bool { return a == f.area }
func (f *fakeSource) close()                     { f.closed = true }

// scriptSources replaces duplication for one test. Every source it hands out
// is recorded; createErr makes creation fail.
func scriptSources(t *testing.T, createErr error) *[]*fakeSource {
	t.Helper()
	made := &[]*fakeSource{}
	prev := newFrameSource
	newFrameSource = func(area MonitorInfo) (frameSource, error) {
		if createErr != nil {
			*made = append(*made, nil)
			return nil, createErr
		}
		f := &fakeSource{area: area}
		*made = append(*made, f)
		return f, nil
	}
	t.Cleanup(func() { newFrameSource = prev })
	return made
}

func capturerOrSkip(t *testing.T) *WindowsCapturer {
	t.Helper()
	c := NewPlatformCapturer().(*WindowsCapturer)
	if caps := c.Capabilities(); !caps.Capture {
		t.Skipf("this host cannot be captured: %s", caps.Reason)
	}
	t.Cleanup(c.releaseCaptureResources)
	return c
}

func isDuplicated(px []byte) bool { return len(px) > 0 && px[0] == 0xAB && px[len(px)-1] == 0xAB }

func TestDuplicationFramesAreUsedWhenAvailable(t *testing.T) {
	made := scriptSources(t, nil)
	c := capturerOrSkip(t)
	px, w, h, err := c.CaptureScreen()
	if err != nil {
		t.Fatal(err)
	}
	if !isDuplicated(px) || len(px) != w*h*4 {
		t.Fatalf("the frame did not come from duplication (%d sources made)", len(*made))
	}
}

// Duplication that cannot start (RDP, a basic display driver, the secure
// desktop) must not cost the operator the session: GDI takes the frame, and
// duplication is not tried again on every tick.
func TestAnUnavailableDuplicationFallsBackToGDIAndBacksOff(t *testing.T) {
	made := scriptSources(t, errors.New("DuplicateOutput failed: HRESULT 0x887A0004"))
	c := capturerOrSkip(t)
	for i := 0; i < 3; i++ {
		px, w, h, err := c.CaptureScreen()
		if err != nil {
			t.Fatalf("capture %d failed instead of falling back to GDI: %v", i, err)
		}
		if isDuplicated(px) || len(px) != w*h*4 {
			t.Fatalf("capture %d: not a GDI frame", i)
		}
	}
	if len(*made) != 1 {
		t.Fatalf("duplication was attempted %d times in three frames; it should wait out the backoff", len(*made))
	}
	c.dxRetryAt = time.Now().Add(-time.Millisecond)
	_, _, _, _ = c.CaptureScreen()
	if len(*made) != 2 {
		t.Fatalf("duplication was not retried once the backoff had passed (%d attempts)", len(*made))
	}
	if c.dxBackoff != 2*dxgiRetryMin {
		t.Fatalf("backoff after two failures is %s, want %s", c.dxBackoff, 2*dxgiRetryMin)
	}
}

// A desktop switch (UAC, Ctrl+Alt+Del) kills the duplication mid-session. That
// frame comes from GDI, the dead duplication is released, and a new one is
// built once the pause is over.
func TestALostDuplicationIsReleasedAndRebuilt(t *testing.T) {
	made := scriptSources(t, nil)
	c := capturerOrSkip(t)
	if px, _, _, _ := c.CaptureScreen(); !isDuplicated(px) {
		t.Fatal("first frame did not use duplication")
	}
	first := (*made)[0]
	first.err = errDuplicationLost
	px, _, _, err := c.CaptureScreen()
	if err != nil || isDuplicated(px) {
		t.Fatalf("a lost duplication did not fall back to a GDI frame (err=%v)", err)
	}
	if !first.closed {
		t.Fatal("the dead duplication was not released")
	}
	c.dxRetryAt = time.Now().Add(-time.Millisecond)
	if px, _, _, _ := c.CaptureScreen(); !isDuplicated(px) || len(*made) != 2 {
		t.Fatalf("duplication was not rebuilt after the pause (%d made)", len(*made))
	}
}

// A duplication that has started but has no image yet is kept: tearing it
// down would restart the wait for the first image on every tick.
func TestADuplicationWithNoFrameYetIsKept(t *testing.T) {
	made := scriptSources(t, nil)
	c := capturerOrSkip(t)
	_, _, _, _ = c.CaptureScreen()
	src := (*made)[0]
	src.err = errNoNewFrame
	px, _, _, err := c.CaptureScreen()
	if err != nil || isDuplicated(px) {
		t.Fatalf("no GDI frame while duplication had nothing (err=%v)", err)
	}
	if src.closed || len(*made) != 1 {
		t.Fatalf("the duplication was dropped (closed=%v, made=%d)", src.closed, len(*made))
	}
}

// The backoff doubles to a ceiling, so a session spent on the lock screen
// tries duplication about twice a minute, not once a frame.
func TestTheDuplicationBackoffIsCapped(t *testing.T) {
	c := &WindowsCapturer{}
	now := time.Now()
	for i := 0; i < 10; i++ {
		c.duplicationFailed(errors.New("x"), now)
	}
	if c.dxBackoff != dxgiRetryMax {
		t.Fatalf("backoff after ten failures is %s, want the %s cap", c.dxBackoff, dxgiRetryMax)
	}
}

// Building a duplication and taking its first frame, over and over, each on a
// new goroutine with a small stack: the path a session start takes. This is an
// endurance check, not a proof that addresses stay put while Windows writes to
// them -- stack moves cannot be forced from a test.
// TestNoCOMHelperCallIsHandedAGoAddress is what guards that.
func TestDuplicationSurvivesAFreshStackEveryTime(t *testing.T) {
	c := capturerOrSkip(t)
	area := c.captureRect()
	if d, err := newDXGIDuplicator(area); err != nil {
		t.Skipf("desktop duplication is unavailable here: %v", err)
	} else {
		d.close()
	}
	for i := 0; i < 40; i++ {
		errc := make(chan error, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					errc <- fmt.Errorf("panic: %v", r)
				}
			}()
			d, err := newDXGIDuplicator(area)
			if err != nil {
				errc <- err
				return
			}
			defer d.close()
			px, err := d.capture()
			if err == nil && len(px) != area.Width*area.Height*4 {
				err = fmt.Errorf("frame is %d bytes", len(px))
			}
			errc <- err
		}()
		if err := <-errc; err != nil && !errors.Is(err, errNoNewFrame) {
			t.Fatalf("capture %d on a fresh goroutine: %v", i, err)
		}
	}
}

// The real thing, where the host has it: duplication and GDI must show the
// same desktop. A few tiles may differ (a clock, a caret), a wrong pixel
// format or row pitch would differ everywhere.
func TestDuplicationAndGDISeeTheSameDesktop(t *testing.T) {
	c := capturerOrSkip(t)
	area := c.captureRect()
	d, err := newDXGIDuplicator(area)
	if err != nil {
		t.Skipf("desktop duplication is unavailable here: %v", err)
	}
	defer d.close()
	dup, err := d.capture()
	if err != nil {
		t.Skipf("desktop duplication produced no frame: %v", err)
	}
	prev := dxgiDisabled
	dxgiDisabled = true
	gdi, w, h, err := c.CaptureScreen()
	dxgiDisabled = prev
	if err != nil {
		t.Fatal(err)
	}
	if len(dup) != len(gdi) {
		t.Fatalf("duplication returned %d bytes, GDI %d, for %dx%d", len(dup), len(gdi), w, h)
	}
	same, tiles := 0, 0
	for ty := 0; ty < h; ty += tileSize {
		for tx := 0; tx < w; tx += tileSize {
			tiles++
			eq := true
			for y := ty; y < min(ty+tileSize, h) && eq; y++ {
				for x := tx; x < min(tx+tileSize, w); x++ {
					i := (y*w + x) * 4
					if dup[i] != gdi[i] || dup[i+1] != gdi[i+1] || dup[i+2] != gdi[i+2] {
						eq = false
						break
					}
				}
			}
			if eq {
				same++
			}
		}
	}
	if same*10 < tiles*9 {
		t.Fatalf("only %d of %d tiles match between duplication and GDI", same, tiles)
	}
}

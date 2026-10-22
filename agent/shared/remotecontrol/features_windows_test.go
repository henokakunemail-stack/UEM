//go:build windows

package remotecontrol

import (
	"testing"
	"time"
)

func TestMonitorsIncludeExactlyOnePrimary(t *testing.T) {
	c := NewPlatformCapturer().(*WindowsCapturer)
	if !c.Capabilities().Capture {
		t.Skip("this host cannot be captured")
	}
	mons := c.Monitors()
	primaries := 0
	for i, m := range mons {
		if m.Index != i || m.Width <= 0 || m.Height <= 0 {
			t.Fatalf("monitor %d is malformed: %+v", i, m)
		}
		if m.Primary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("%d primary monitors in %+v", primaries, mons)
	}
}

// Selecting a monitor has to change what is captured, not just what is
// reported: a frame of the wrong size lands at the wrong scale on the console
// and every click lands somewhere else.
func TestSelectedMonitorDecidesTheFrameSize(t *testing.T) {
	c := NewPlatformCapturer().(*WindowsCapturer)
	if !c.Capabilities().Capture {
		t.Skip("this host cannot be captured")
	}
	defer c.releaseCaptureResources()

	for _, m := range c.Monitors() {
		if _, err := c.SelectMonitor(m.Index); err != nil {
			t.Fatal(err)
		}
		_, w, h, err := c.CaptureScreen()
		if err != nil {
			t.Fatal(err)
		}
		if w != m.Width || h != m.Height {
			t.Fatalf("monitor %d is %dx%d but the frame was %dx%d", m.Index, m.Width, m.Height, w, h)
		}
		if ox, oy := c.origin(); ox != m.X || oy != m.Y {
			t.Fatalf("monitor %d starts at %d,%d but input is offset by %d,%d", m.Index, m.X, m.Y, ox, oy)
		}
	}

	all, err := c.SelectMonitor(monitorAll)
	if err != nil {
		t.Fatal(err)
	}
	if _, w, h, err := c.CaptureScreen(); err != nil || w != all.Width || h != all.Height {
		t.Fatalf("all monitors: frame %dx%d err=%v, want %dx%d", w, h, err, all.Width, all.Height)
	}
	if _, err := c.SelectMonitor(99); err == nil {
		t.Fatal("a monitor that does not exist was accepted")
	}
}

// The operator's paste must land, and must not come straight back to them as
// an endpoint clipboard change -- the sequence number the write returns is how
// the session tells the two apart.
func TestClipboardRoundTripsAndReportsItsOwnSequence(t *testing.T) {
	c := NewPlatformCapturer().(*WindowsCapturer)
	if !c.Capabilities().Capture {
		t.Skip("this host cannot be captured")
	}
	saved, _ := c.ClipboardText()
	defer func() { _, _ = c.SetClipboardText(saved) }()

	want := "remote-control clipboard test ✓ " + time.Now().Format(time.RFC3339Nano)
	seq, err := c.SetClipboardText(want)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ClipboardSequence(); got != seq {
		t.Fatalf("sequence after our own write is %d, but the write reported %d", got, seq)
	}
	got, err := c.ClipboardText()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("clipboard holds %q, want %q", got, want)
	}
}

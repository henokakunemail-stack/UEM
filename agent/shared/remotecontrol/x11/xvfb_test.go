//go:build linux

package x11

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// StartXvfb runs a private Xvfb for a test and returns its DISPLAY. The test
// is skipped where Xvfb is not installed; the Linux CI runner has it.
func startXvfb(t *testing.T, w, h int) string {
	t.Helper()
	bin, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Skip("Xvfb is not installed")
	}
	for n := 90; n < 110; n++ {
		sock := fmt.Sprintf("/tmp/.X11-unix/X%d", n)
		if _, err := os.Stat(sock); err == nil {
			continue
		}
		cmd := exec.Command(bin, fmt.Sprintf(":%d", n), "-screen", "0", fmt.Sprintf("%dx%dx24", w, h), "-nolisten", "tcp")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(sock); err == nil {
				return fmt.Sprintf(":%d", n)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("Xvfb on :%d did not come up", n)
	}
	t.Fatal("no free display number")
	return ""
}

func TestXvfbCaptureInputAndWindows(t *testing.T) {
	display := startXvfb(t, 640, 480)
	xc, err := Dial(display, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer xc.Close()
	if xc.Screen.Width != 640 || xc.Screen.Height != 480 {
		t.Fatalf("screen %dx%d", xc.Screen.Width, xc.Screen.Height)
	}

	// Paint a red square on the root window and read it back.
	gc, err := xc.CreateGC(xc.Screen.Root, 0xff0000, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := xc.FillRect(xc.Screen.Root, gc, 10, 10, 50, 50); err != nil {
		t.Fatal(err)
	}
	if err := xc.Sync(); err != nil {
		t.Fatal(err)
	}
	px, err := xc.GetImage(20, 20, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if px[0] != 0 || px[1] != 0 || px[2] != 0xff || px[3] != 0xff {
		t.Fatalf("pixel BGRA = %x, want 0000ffff (red)", px[:4])
	}

	// Move the pointer through XTEST and see it moved.
	if err := xc.XTestInit(); err != nil {
		t.Fatal(err)
	}
	if err := xc.FakeInput(MotionNotify, 0, 100, 120); err != nil {
		t.Fatal(err)
	}
	if err := xc.Sync(); err != nil {
		t.Fatal(err)
	}
	if x, y, _, err := xc.QueryPointer(); err != nil || x != 100 || y != 120 {
		t.Fatalf("pointer at %d,%d (%v), want 100,120", x, y, err)
	}

	// Press and release "a" and watch the key state.
	per, syms, err := xc.KeyboardMapping()
	if err != nil {
		t.Fatal(err)
	}
	code := NewKeymap(xc.MinKeycode, per, syms).Keycode(VKToKeysym(0x41))
	if code == 0 {
		t.Fatal("no keycode for 'a'")
	}
	down := func() bool {
		keys, err := xc.QueryKeymap()
		if err != nil {
			t.Fatal(err)
		}
		return keys[code/8]&(1<<(code%8)) != 0
	}
	_ = xc.FakeInput(KeyPress, code, 0, 0)
	_ = xc.Sync()
	if !down() {
		t.Fatal("key not down after XTEST KeyPress")
	}
	_ = xc.FakeInput(KeyRelease, code, 0, 0)
	_ = xc.Sync()
	if down() {
		t.Fatal("key still down after XTEST KeyRelease")
	}

	// An override-redirect window (the session banner) shows in the capture.
	win, err := xc.CreateOverrideWindow(300, 0, 40, 20, 0x00ff00)
	if err != nil {
		t.Fatal(err)
	}
	if err := xc.MapRaised(win); err != nil {
		t.Fatal(err)
	}
	font, err := xc.OpenFont("fixed")
	if err != nil {
		t.Fatal(err)
	}
	tgc, err := xc.CreateGC(win, 0xffffff, 0x00ff00, font)
	if err != nil {
		t.Fatal(err)
	}
	_ = xc.DrawText(win, tgc, 2, 14, "hi")
	if err := xc.Sync(); err != nil {
		t.Fatalf("drawing the banner: %v", err)
	}
	px, err = xc.GetImage(338, 18, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if px[0] != 0 || px[1] != 0xff || px[2] != 0 {
		t.Fatalf("banner pixel BGRA = %x, want green", px[:4])
	}

	if mons, err := xc.Monitors(); err == nil {
		if len(mons) == 0 || mons[0].Width != 640 {
			t.Fatalf("monitors = %+v", mons)
		}
	} else {
		t.Logf("RandR monitors unavailable on this Xvfb: %v", err)
	}
}

//go:build linux

package remotecontrol

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The capturer end to end against a real X server: it finds the display
// through a /proc scan the way the root agent does, captures, injects and
// reports the pointer. Skipped where Xvfb is not installed.
func TestLinuxCapturerAgainstXvfb(t *testing.T) {
	bin, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Skip("Xvfb is not installed")
	}
	n := 0
	for d := 110; d < 130; d++ {
		if _, err := os.Stat(fmt.Sprintf("/tmp/.X11-unix/X%d", d)); err != nil {
			n = d
			break
		}
	}
	cmd := exec.Command(bin, fmt.Sprintf(":%d", n), "-screen", "0", "800x600x24", "-nolisten", "tcp")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(fmt.Sprintf("/tmp/.X11-unix/X%d", n)); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A fake /proc with one desktop process of a logged-in user.
	root := t.TempDir()
	dir := filepath.Join(root, "4242")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "environ"), []byte(fmt.Sprintf("DISPLAY=:%d\x00XDG_SESSION_TYPE=x11\x00", n)), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "status"), []byte("Uid:\t1000\t1000\t1000\t1000\n"), 0o644)
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })

	c := NewPlatformCapturer().(*LinuxCapturer)
	caps := c.Capabilities()
	if !caps.Capture || !caps.Mouse || !caps.Keyboard {
		t.Fatalf("capabilities = %+v", caps)
	}
	img, w, h, err := c.CaptureScreen()
	if err != nil || w != 800 || h != 600 || len(img) != 800*600*4 {
		t.Fatalf("capture %dx%d len %d err %v", w, h, len(img), err)
	}
	if err := c.InjectMouseEvent(InputEvent{Type: "mouse", Action: "move", X: 321, Y: 123}); err != nil {
		t.Fatal(err)
	}
	if x, y, visible := c.CursorPosition(); x != 321 || y != 123 || !visible {
		t.Fatalf("cursor %d,%d visible=%v", x, y, visible)
	}
	if err := c.InjectKeyboardEvent(InputEvent{Type: "keyboard", Action: "down", Code: 0x10}); err != nil {
		t.Fatal(err)
	}
	if len(c.held) != 1 {
		t.Fatalf("held keys = %v", c.held)
	}
	_ = c.ReleaseAllKeys()
	if len(c.held) != 0 {
		t.Fatal("ReleaseAllKeys left keys held")
	}

	// A Wayland desktop is reported, with the way out, instead of capturing black.
	_ = os.WriteFile(filepath.Join(dir, "environ"), []byte("XDG_SESSION_TYPE=wayland\x00WAYLAND_DISPLAY=wayland-0\x00"), 0o644)
	w2 := NewPlatformCapturer()
	if caps := w2.Capabilities(); caps.Capture || caps.Reason == "" {
		t.Fatalf("wayland capabilities = %+v", caps)
	}
}

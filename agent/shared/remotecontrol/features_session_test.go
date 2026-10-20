package remotecontrol

import (
	"encoding/base64"
	"os"
	"testing"
)

// featureCapturer adds the optional features to fakeCapturer, recording what
// the session asked of them.
type featureCapturer struct {
	fakeCapturer
	selected  []int
	clipboard []string
	seq       uint32
}

func (f *featureCapturer) Monitors() []MonitorInfo {
	return []MonitorInfo{{Index: 0, Width: 8, Height: 4, Primary: true}, {Index: 1, X: 8, Width: 8, Height: 4}}
}

func (f *featureCapturer) SelectMonitor(i int) (MonitorInfo, error) {
	f.selected = append(f.selected, i)
	return f.Monitors()[0], nil
}

func (f *featureCapturer) ClipboardSequence() uint32      { return f.seq }
func (f *featureCapturer) ClipboardText() (string, error) { return "", nil }
func (f *featureCapturer) SetClipboardText(text string) (uint32, error) {
	f.clipboard = append(f.clipboard, text)
	f.seq++
	return f.seq, nil
}

func newFeatureSession(t *testing.T, mode string) (*Session, *featureCapturer) {
	t.Helper()
	c := &featureCapturer{fakeCapturer: fakeCapturer{capture: true}}
	s := NewSession(SessionConfig{SessionID: "s-1", Mode: mode}, "http://localhost:1", Credentials{}, c)
	s.files = newFileReceiver(t.TempDir())
	return s, c
}

// View-only means the endpoint is not changed. Picking which monitor to watch
// changes nothing on it and must still work; pasting into its clipboard, typing
// a key combination or dropping a file onto it must not. The relay gates these
// too -- this is the agent's own gate, which is what holds if the relay's
// mode ever disagrees with the agent's.
func TestViewOnlyAllowsMonitorSelectionAndNothingThatChangesTheEndpoint(t *testing.T) {
	s, c := newFeatureSession(t, "view_only")

	for _, in := range []InputEvent{
		{Type: "monitor", Index: 1},
		{Type: "clipboard", Text: "secret"},
		{Type: "keys", Combo: "win_r"},
		{Type: "file_begin", ID: "f", Name: "x.txt", Size: 1},
	} {
		if !s.handleFeatureMessage(in) {
			t.Fatalf("%s was not recognised as a feature message", in.Type)
		}
	}
	if len(c.selected) != 1 || c.selected[0] != 1 {
		t.Fatalf("monitor selection in view_only: %v", c.selected)
	}
	if len(c.clipboard) != 0 || len(c.keyEvents) != 0 {
		t.Fatalf("view_only changed the endpoint: clipboard=%v keys=%v", c.clipboard, c.keyEvents)
	}
	if entries, _ := os.ReadDir(s.files.dir); len(entries) != 0 {
		t.Fatalf("view_only accepted a file: %v", entries)
	}
}

func TestFullControlAppliesClipboardKeysAndFiles(t *testing.T) {
	s, c := newFeatureSession(t, "full_control")

	s.handleFeatureMessage(InputEvent{Type: "clipboard", Text: "hello"})
	s.handleFeatureMessage(InputEvent{Type: "keys", Combo: "alt_tab"})
	s.handleFeatureMessage(InputEvent{Type: "file_begin", ID: "f", Name: "note.txt", Size: 2})
	s.handleFeatureMessage(InputEvent{Type: "file_chunk", ID: "f", Seq: 0, Data: base64.StdEncoding.EncodeToString([]byte("hi"))})
	s.handleFeatureMessage(InputEvent{Type: "file_end", ID: "f"})

	if len(c.clipboard) != 1 || c.clipboard[0] != "hello" {
		t.Fatalf("clipboard writes: %v", c.clipboard)
	}
	if len(c.keyEvents) != 4 {
		t.Fatalf("alt_tab produced %d key events, want 4", len(c.keyEvents))
	}
	if b, err := os.ReadFile(s.files.dir + string(os.PathSeparator) + "note.txt"); err != nil || string(b) != "hi" {
		t.Fatalf("uploaded file: %q err=%v", b, err)
	}
}

// Mouse and keyboard are not feature messages; claiming them would bypass the
// session's ordinary input path.
func TestOrdinaryInputIsNotClaimedAsAFeature(t *testing.T) {
	s, _ := newFeatureSession(t, "full_control")
	for _, typ := range []string{"mouse", "keyboard", "mode", "refresh"} {
		if s.handleFeatureMessage(InputEvent{Type: typ}) {
			t.Fatalf("%s was claimed as a feature message", typ)
		}
	}
}

// The hello advertises only what the capturer implements, and nothing at all
// for a host that cannot capture.
func TestHelloAdvertisesOptionalFeaturesOnlyWhenCapturing(t *testing.T) {
	s, _ := newFeatureSession(t, "full_control")
	caps := s.capabilities()
	if len(caps.Monitors) != 2 || !caps.Clipboard || !caps.FileTransfer || caps.MaxFileBytes != maxFileBytes {
		t.Fatalf("capable host advertised %+v", caps)
	}

	plain := NewSession(SessionConfig{SessionID: "s-2"}, "http://localhost:1", Credentials{}, &fakeCapturer{capture: false})
	if caps := plain.capabilities(); caps.Clipboard || caps.FileTransfer || len(caps.Monitors) != 0 {
		t.Fatalf("a host that cannot capture advertised %+v", caps)
	}
}

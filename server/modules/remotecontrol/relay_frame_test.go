package remotecontrol

import (
	"testing"
	"time"
)

// A full 1080p keyframe is megabytes. The old flat 2.5s budget timed it out on
// a slow link, and a timed-out gorilla connection never recovers, so the session
// died on its first full frame.
func TestFrameWriteDeadlineGrowsWithTheFrame(t *testing.T) {
	small := frameWriteDeadline(20 * 1024)
	keyframe := frameWriteDeadline(3 * 1024 * 1024)
	if small != 2500*time.Millisecond {
		t.Fatalf("a small damage rect got %v, want 2.5s", small)
	}
	if keyframe < 20*time.Second {
		t.Fatalf("a 3MB keyframe got %v, which a 1Mbps link cannot meet", keyframe)
	}
	if huge := frameWriteDeadline(100 * 1024 * 1024); huge != 30*time.Second {
		t.Fatalf("the budget is not capped: %v", huge)
	}
}

// A refresh and a monitor selection change only what the operator sees, so they
// have to be recognised before the view_only gate drops them. Clipboard text,
// key combinations and file chunks change the endpoint and must not be.
func TestViewControlMessagesAreRecognised(t *testing.T) {
	for _, m := range []string{`{"type":"refresh"}`, `{"type":"monitor","index":1}`} {
		if !isViewControlMessage([]byte(m)) {
			t.Fatalf("%s not recognised", m)
		}
	}
	for _, m := range []string{
		`{"type":"mouse","action":"down"}`, `not json`, `{"type":"mode","mode":"view_only"}`,
		`{"type":"clipboard","text":"x"}`, `{"type":"keys","combo":"win"}`, `{"type":"file_chunk","id":"f"}`,
	} {
		if isViewControlMessage([]byte(m)) {
			t.Fatalf("%s let through the view_only gate", m)
		}
	}
}

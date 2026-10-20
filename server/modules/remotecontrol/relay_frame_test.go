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

// A refresh is a keyframe request, not input, so it has to be recognised before
// the view_only gate drops it.
func TestRefreshIsRecognisedAsAControlMessage(t *testing.T) {
	if !isRefreshMessage([]byte(`{"type":"refresh"}`)) {
		t.Fatal("refresh not recognised")
	}
	for _, m := range []string{`{"type":"mouse","action":"down"}`, `not json`, `{"type":"mode","mode":"view_only"}`} {
		if isRefreshMessage([]byte(m)) {
			t.Fatalf("%s treated as a refresh", m)
		}
	}
}

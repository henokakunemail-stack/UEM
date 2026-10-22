package remotecontrol

import (
	"strings"
	"testing"
)

// A combination is typed the way a person types it: presses in order, releases
// in reverse. Releasing Alt before Tab turns Alt+Tab into a bare Tab, and a
// press with no release leaves Win held down across the endpoint.
func TestKeyCombosReleaseInReverseAndBalance(t *testing.T) {
	for combo := range keyCombos {
		events, err := comboEvents(combo)
		if err != nil {
			t.Fatal(err)
		}
		held := map[int]bool{}
		var order []int
		for _, e := range events {
			switch e.Action {
			case "down":
				held[e.Code] = true
				order = append(order, e.Code)
			case "up":
				if !held[e.Code] {
					t.Fatalf("%s: key %#x released without a press", combo, e.Code)
				}
				if last := order[len(order)-1]; last != e.Code {
					t.Fatalf("%s: released %#x while %#x was pressed after it", combo, e.Code, last)
				}
				order = order[:len(order)-1]
				delete(held, e.Code)
			}
		}
		if len(held) != 0 {
			t.Fatalf("%s leaves keys held: %v", combo, held)
		}
	}
	if _, err := comboEvents("ctrl_alt_del"); err == nil {
		t.Fatal("an unsupported combination was accepted")
	}
}

func TestClipboardTextIsCappedOnACharacterBoundary(t *testing.T) {
	text := strings.Repeat("é", maxClipboardBytes) // 2 bytes each
	got := truncateUTF8(text, maxClipboardBytes+1)
	if len(got) > maxClipboardBytes+1 || !strings.HasSuffix(got, "é") {
		t.Fatalf("truncated to %d bytes ending %q", len(got), got[len(got)-2:])
	}
}

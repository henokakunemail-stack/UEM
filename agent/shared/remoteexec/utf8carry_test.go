package remoteexec

import (
	"strings"
	"testing"
)

// Every split point of a string full of multi-byte characters must come back
// whole, with no replacement characters, once the pieces are joined.
func TestUTF8CarryNeverSplitsACharacter(t *testing.T) {
	in := "héllo ─┼─ 日本語 🙂 done"
	b := []byte(in)
	for i := 0; i <= len(b); i++ {
		for j := i; j <= len(b); j++ {
			var c utf8Carry
			var out strings.Builder
			for _, part := range [][]byte{b[:i], b[i:j], b[j:]} {
				s := c.feed(part)
				if strings.ContainsRune(s, '�') {
					t.Fatalf("split %d/%d produced a replacement character in %q", i, j, s)
				}
				out.WriteString(s)
			}
			out.WriteString(c.flush())
			if out.String() != in {
				t.Fatalf("split %d/%d: got %q, want %q", i, j, out.String(), in)
			}
		}
	}
}

func TestUTF8CarryHoldsBackOnlyTheUnfinishedTail(t *testing.T) {
	var c utf8Carry
	// "é" is C3 A9; send the lead byte alone after some ASCII.
	if got := c.feed([]byte{'a', 'b', 0xC3}); got != "ab" {
		t.Fatalf("feed = %q, want %q", got, "ab")
	}
	if got := c.feed([]byte{0xA9, 'c'}); got != "éc" {
		t.Fatalf("feed = %q, want %q", got, "éc")
	}
}

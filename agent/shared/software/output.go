package software

import (
	"bytes"
	"unicode/utf16"
)

// decodeOutput turns an installer process's raw stdout/stderr into text the
// console can display.
//
// Windows console programs -- msiexec above all -- write UTF-16LE, so every
// ASCII character arrives as two bytes with a NUL between them. Storing those
// bytes raw (which every runner did via string(outBytes)) fills the View Logs
// box with invisible NULs and leaves the operator unable to read why the
// install failed. Unix tools emit UTF-8, so this has to sniff rather than
// assume per-OS.
//
// ponytail: this detects ASCII-range UTF-16 without a BOM, which is what
// installer diagnostics actually are. BOM-less UTF-16 carrying non-ASCII is
// not distinguishable from UTF-8 by any reliable test, so it is left as bytes
// rather than guessed at; a Unicode-aware program emits a BOM, and that path
// is handled above.
func decodeOutput(b []byte) string {
	switch {
	case len(b) == 0:
		return ""
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return decodeUTF16(b[2:], false)
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return decodeUTF16(b[2:], true)
	case looksUTF16LE(b):
		return decodeUTF16(b, false)
	}
	return string(b)
}

// looksUTF16LE reports whether b is BOM-less UTF-16LE carrying ASCII text: a
// NUL high byte and a printable low byte in every code unit. The scan stops at
// a NUL unit, so a trailing terminator does not disqualify the stream, and two
// units is the floor because rewriting a one-unit buffer is more risk than it
// is worth.
func looksUTF16LE(b []byte) bool {
	units := 0
	for i := 0; i+1 < len(b) && units < 64; i += 2 {
		low, high := b[i], b[i+1]
		if low == 0x00 && high == 0x00 {
			break // terminator
		}
		if high != 0x00 || low < 0x20 || low > 0x7E {
			return false
		}
		units++
	}
	return units >= 2
}

// decodeUTF16 converts UTF-16 code units to a string, stopping at a NUL unit.
// utf16.Decode joins surrogate pairs and substitutes any unpaired one.
func decodeUTF16(b []byte, bigEndian bool) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		var c uint16
		if bigEndian {
			c = uint16(b[i])<<8 | uint16(b[i+1])
		} else {
			c = uint16(b[i+1])<<8 | uint16(b[i])
		}
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

package remoteexec

import "unicode/utf8"

// utf8Carry turns a byte stream into strings without splitting a character.
//
// Terminal output travels as a JSON string, and encoding/json replaces invalid
// UTF-8 with U+FFFD. A read that ends halfway through "é" or a box-drawing
// character would therefore arrive as two replacement glyphs, which in a
// full-screen program like htop scatters garbage across the frame. The
// incomplete tail of each read is held back and prefixed to the next one.
type utf8Carry struct {
	pending []byte
}

// feed returns everything up to the last complete character.
func (c *utf8Carry) feed(b []byte) string {
	data := append(c.pending, b...)
	cut := len(data)
	// A character is at most 4 bytes, so only the last 3 can be an unfinished
	// prefix. Walk back to the last start byte and see whether it is complete.
	for i := len(data) - 1; i >= 0 && i >= len(data)-3; i-- {
		if utf8.RuneStart(data[i]) {
			if !utf8.FullRune(data[i:]) {
				cut = i
			}
			break
		}
	}
	out := string(data[:cut])
	c.pending = append(c.pending[:0:0], data[cut:]...)
	return out
}

// flush returns whatever is held back, complete or not, at end of stream.
func (c *utf8Carry) flush() string {
	out := string(c.pending)
	c.pending = nil
	return out
}

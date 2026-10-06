//go:build windows

package update

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestTheFormatCheckItself exercises the verifier directly, on both the shape
// it must accept and the shapes it must reject. The tests in verify_test.go go
// through the whole engine on this machine's platform; this one covers the PE
// branch, which is the only format verifySwappedBinary accepts on Windows.
//
// It is tagged accordingly: valid() below builds an MZ/PE image, and the reject
// list carries ELF header bytes. On Linux those two are the same bytes, so the
// "header-only" case is simultaneously the accepted image and a rejected one and
// the assertion cannot hold.
func TestTheFormatCheckItself(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A minimal but structurally correct PE image.
	valid := func() []byte {
		b := make([]byte, 0x100)
		b[0], b[1] = 'M', 'Z'
		binary.LittleEndian.PutUint32(b[0x3c:], 0x80)
		copy(b[0x80:], []byte{'P', 'E', 0, 0})
		return b
	}

	if err := verifySwappedBinary(write("good", valid())); err != nil {
		t.Errorf("a well-formed image was rejected: %v", err)
	}

	rejects := map[string][]byte{
		"text.txt":    []byte("this is not a program; it will not boot\n"),
		"empty.bin":   {},
		"zeros.bin":   make([]byte, 4096),
		"almost-mz":   append([]byte{'M', 'Z'}, make([]byte, 200)...),
		"truncated":   valid()[:2],
		"header-only": {'M', 'Z'},
	}
	for name, content := range rejects {
		if err := verifySwappedBinary(write(name, content)); err == nil {
			t.Errorf("%s was accepted; it is not a loadable image for any platform", name)
		}
	}
}

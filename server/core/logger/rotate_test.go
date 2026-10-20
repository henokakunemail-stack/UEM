package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The file is cut at the limit and only keep old files survive, so a busy
// server's log cannot fill the disk the database lives on.
func TestLogFileRotatesAndKeepsOnlyTheNewest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")
	rf, err := openRotating(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes
	for i := 0; i < 20; i++ {
		if _, err := rf.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	rf.f.Close()

	for _, name := range []string{"server.log", "server.log.1", "server.log.2"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
		if fi.Size() > 100 {
			t.Fatalf("%s is %d bytes, over the 100-byte limit", name, fi.Size())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "server.log.3")); err == nil {
		t.Fatal("more than keep=2 old files were kept")
	}
}

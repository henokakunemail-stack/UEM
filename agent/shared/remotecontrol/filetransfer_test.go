package remotecontrol

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func send(t *testing.T, r *fileReceiver, id, name string, parts ...string) (string, error) {
	t.Helper()
	size := 0
	for _, p := range parts {
		size += len(p)
	}
	if err := r.begin(id, name, int64(size)); err != nil {
		return "", err
	}
	for i, p := range parts {
		if _, err := r.chunk(id, i, base64.StdEncoding.EncodeToString([]byte(p))); err != nil {
			return "", err
		}
	}
	return r.end(id)
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range des {
		names = append(names, d.Name())
	}
	return names
}

// The operator chooses the name, and the name is not a path. A traversal that
// reached outside the transfers folder would let any console user write over
// the endpoint user's own files -- or their startup folder.
func TestUploadNameCannotEscapeTheTransfersFolder(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Remote Transfers")
	r := newFileReceiver(dir)

	for i, name := range []string{`..\..\evil.txt`, `../../evil.txt`, `C:\Windows\evil.txt`, `/etc/evil.txt`} {
		path, err := send(t, r, string(rune('a'+i)), name, "x")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if filepath.Dir(path) != dir {
			t.Fatalf("%q was written to %s, outside %s", name, path, dir)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "evil.txt")); err == nil {
		t.Fatal("a traversal wrote into the parent folder")
	}
}

func TestUploadNeverOverwritesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	r := newFileReceiver(dir)
	first, err := send(t, r, "1", "report.pdf", "one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := send(t, r, "2", "report.pdf", "two")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(second) != "report (1).pdf" {
		t.Fatalf("second upload landed at %s, want report (1).pdf", second)
	}
	if b, _ := os.ReadFile(first); string(b) != "one" {
		t.Fatalf("first upload was overwritten: %q", b)
	}
}

func TestReservedAndHostileNamesAreMadeSafe(t *testing.T) {
	cases := map[string]string{
		"CON.txt":        "_CON.txt",
		"a<b>c:d|e?.txt": "a_b_c_d_e_.txt",
		"invoice.exe. ":  "invoice.exe",
		"..":             "file",
		"":               "file",
		"name\x00.txt":   "name_.txt",
	}
	for in, want := range cases {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A missing chunk is a corrupt file. It has to abort, and leave nothing behind.
func TestOutOfOrderChunkAbortsAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	r := newFileReceiver(dir)
	if err := r.begin("x", "a.bin", 6); err != nil {
		t.Fatal(err)
	}
	if _, err := r.chunk("x", 1, base64.StdEncoding.EncodeToString([]byte("abc"))); err == nil {
		t.Fatal("chunk 1 was accepted before chunk 0")
	}
	if names := entries(t, dir); len(names) != 0 {
		t.Fatalf("aborted transfer left %v", names)
	}
}

func TestShortOrOversizedUploadIsRejected(t *testing.T) {
	dir := t.TempDir()
	r := newFileReceiver(dir)

	if err := r.begin("big", "a", maxFileBytes+1); err == nil {
		t.Fatal("a file over the limit was accepted")
	}

	if err := r.begin("s", "short.bin", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := r.chunk("s", 0, base64.StdEncoding.EncodeToString([]byte("abc"))); err != nil {
		t.Fatal(err)
	}
	if _, err := r.end("s"); err == nil || !strings.Contains(err.Error(), "3 of 10") {
		t.Fatalf("a short upload ended with err=%v", err)
	}

	if err := r.begin("o", "over.bin", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := r.chunk("o", 0, base64.StdEncoding.EncodeToString([]byte("abc"))); err == nil {
		t.Fatal("more bytes than declared were accepted")
	}
	if names := entries(t, dir); len(names) != 0 {
		t.Fatalf("rejected uploads left %v", names)
	}
}

// A session that ends mid-upload must not leave half a file on the endpoint.
func TestCloseAllRemovesPartialFiles(t *testing.T) {
	dir := t.TempDir()
	r := newFileReceiver(dir)
	if err := r.begin("p", "partial.bin", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := r.chunk("p", 0, base64.StdEncoding.EncodeToString([]byte("abc"))); err != nil {
		t.Fatal(err)
	}
	r.closeAll()
	if names := entries(t, dir); len(names) != 0 {
		t.Fatalf("closeAll left %v", names)
	}
}

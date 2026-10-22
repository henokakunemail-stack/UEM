// Command packdist writes a release archive (.tar.gz or .zip) with the file
// modes a Unix host needs.
//
//	go run ./scripts/packdist -o dist/endpoint-server-linux-arm64.tar.gz \
//	    dist/server-linux-arm64=endpoint-server:x \
//	    packaging/linux/scripts/install-server.sh=install-server.sh:x \
//	    dist/agent-linux-amd64=agents/agent-linux-amd64:x
//
// Each argument is SOURCE=NAME, where NAME is the path inside the archive and
// a ":x" suffix marks it executable (0755; everything else is 0644).
//
// It exists because the release is built on Windows, and tar.exe there records
// every file as 0666: the server binary and the install scripts unpack without
// the execute bit (so `./install.sh` fails with "permission denied") and
// group- and world-writable. Archives written here are byte-for-byte the same
// on any host, so the Windows publish script and the CI jobs agree.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

type entry struct {
	src, name string
	exec      bool
}

func main() {
	out := flag.String("o", "", "archive to write (.tar.gz or .zip)")
	flag.Parse()
	if *out == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: packdist -o OUT.tar.gz|OUT.zip SRC=NAME[:x] ...")
		os.Exit(2)
	}
	entries, err := parse(flag.Args())
	if err == nil {
		err = write(*out, entries)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "packdist:", err)
		_ = os.Remove(*out)
		os.Exit(1)
	}
}

func parse(args []string) ([]entry, error) {
	var es []entry
	seen := map[string]bool{}
	for _, a := range args {
		src, name, ok := strings.Cut(a, "=")
		if !ok || src == "" || name == "" {
			return nil, fmt.Errorf("argument %q is not SOURCE=NAME", a)
		}
		e := entry{src: src}
		if strings.HasSuffix(name, ":x") {
			e.exec = true
			name = strings.TrimSuffix(name, ":x")
		}
		e.name = path.Clean(strings.ReplaceAll(name, `\`, "/"))
		if strings.HasPrefix(e.name, "../") || e.name == ".." || path.IsAbs(e.name) {
			return nil, fmt.Errorf("archive name %q leaves the archive root", name)
		}
		if seen[e.name] {
			return nil, fmt.Errorf("archive name %q is given twice", e.name)
		}
		seen[e.name] = true
		es = append(es, e)
	}
	sort.Slice(es, func(i, j int) bool { return es[i].name < es[j].name })
	return es, nil
}

// Every entry carries the same timestamp, so an archive's bytes depend only
// on its contents.
var stamp = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func mode(e entry) int64 {
	if e.exec {
		return 0o755
	}
	return 0o644
}

func write(out string, es []entry) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	switch {
	case strings.HasSuffix(out, ".tar.gz"), strings.HasSuffix(out, ".tgz"):
		err = writeTar(f, es)
	case strings.HasSuffix(out, ".zip"):
		err = writeZip(f, es)
	default:
		err = fmt.Errorf("%s: unknown archive type (want .tar.gz or .zip)", out)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// dirsOf lists the parent directories of every entry, outermost first, so
// they are created with 0755 rather than whatever the extractor defaults to.
func dirsOf(es []entry) []string {
	set := map[string]bool{}
	for _, e := range es {
		for d := path.Dir(e.name); d != "." && d != "/"; d = path.Dir(d) {
			set[d] = true
		}
	}
	dirs := make([]string, 0, len(set))
	for d := range set {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}

func writeTar(w io.Writer, es []entry) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, d := range dirsOf(es) {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeDir, Name: d + "/", Mode: 0o755, ModTime: stamp, Format: tar.FormatPAX,
		}); err != nil {
			return err
		}
	}
	for _, e := range es {
		src, err := os.Open(e.src)
		if err != nil {
			return err
		}
		st, err := src.Stat()
		if err == nil && !st.Mode().IsRegular() {
			err = fmt.Errorf("%s is not a regular file", e.src)
		}
		if err == nil {
			err = tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeReg, Name: e.name, Mode: mode(e), Size: st.Size(),
				ModTime: stamp, Format: tar.FormatPAX,
			})
		}
		if err == nil {
			_, err = io.Copy(tw, src)
		}
		_ = src.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", e.src, err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func writeZip(w io.Writer, es []entry) error {
	zw := zip.NewWriter(w)
	for _, e := range es {
		src, err := os.Open(e.src)
		if err != nil {
			return err
		}
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: stamp}
		h.SetMode(os.FileMode(mode(e)))
		dst, err := zw.CreateHeader(h)
		if err == nil {
			_, err = io.Copy(dst, src)
		}
		_ = src.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", e.src, err)
		}
	}
	return zw.Close()
}

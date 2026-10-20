package remotecontrol

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// maxFileBytes caps one operator upload. It is advertised in the hello so the
// console can refuse a larger file before sending a byte of it.
const maxFileBytes int64 = 200 << 20

// maxOpenTransfers bounds the half-written files one session can hold open.
const maxOpenTransfers = 4

// transfersDirName is created under the endpoint user's Downloads folder. A
// folder of its own keeps operator uploads from landing in the middle of the
// user's own files, and makes them easy to find and to clean up.
const transfersDirName = "Remote Transfers"

// fileReceiver assembles operator uploads on the endpoint.
//
// The name an operator sends is never trusted as a path. It is reduced to its
// last element, stripped of characters Windows refuses or treats specially, and
// written next to nothing it could overwrite: a name that already exists gets a
// " (1)" suffix, the way a browser download does. Bytes go to a ".part" file that
// is renamed only once the declared size has arrived in order, so a session that
// drops mid-upload never leaves a truncated file under the real name.
type fileReceiver struct {
	dir string

	mu        sync.Mutex
	transfers map[string]*incomingFile
}

type incomingFile struct {
	name     string // final file name, already sanitised
	partPath string
	f        *os.File
	size     int64
	received int64
	nextSeq  int
}

func newFileReceiver(dir string) *fileReceiver {
	return &fileReceiver{dir: dir, transfers: make(map[string]*incomingFile)}
}

// defaultTransfersDir is the logged-on user's Downloads\Remote Transfers. The
// worker runs as that user, so the home directory is theirs, not SYSTEM's.
func defaultTransfersDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), transfersDirName)
	}
	downloads := filepath.Join(home, "Downloads")
	if st, err := os.Stat(downloads); err == nil && st.IsDir() {
		return filepath.Join(downloads, transfersDirName)
	}
	return filepath.Join(home, transfersDirName)
}

// sanitizeFileName turns whatever the console sent into a single safe name.
func sanitizeFileName(name string) string {
	// Both separators, whatever this platform calls one: a Windows endpoint
	// must not honour "../" and a Linux one must not keep "..\" as a name.
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20, strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	// Windows silently drops trailing dots and spaces, which would make
	// "report.exe." become "report.exe" on disk.
	clean := strings.TrimRight(strings.TrimSpace(b.String()), ". ")
	if clean == "" || clean == "." || clean == ".." {
		return "file"
	}
	base := strings.ToUpper(strings.TrimSuffix(clean, filepath.Ext(clean)))
	switch base {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		clean = "_" + clean
	}
	if len(clean) > 200 {
		ext := filepath.Ext(clean)
		if len(ext) > 20 {
			ext = ""
		}
		clean = clean[:200-len(ext)] + ext
	}
	return clean
}

// begin opens a transfer. The id is chosen by the console and only has to be
// unique within the session.
func (r *fileReceiver) begin(id, name string, size int64) error {
	if id == "" {
		return errors.New("transfer id is required")
	}
	if size < 0 || size > maxFileBytes {
		return fmt.Errorf("file is %d bytes; the limit is %d", size, maxFileBytes)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.transfers[id]; exists {
		return fmt.Errorf("transfer %q is already in progress", id)
	}
	if len(r.transfers) >= maxOpenTransfers {
		return fmt.Errorf("%d uploads are already in progress", maxOpenTransfers)
	}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", r.dir, err)
	}

	clean := sanitizeFileName(name)
	// O_EXCL makes the .part name a claim: two uploads of the same file in one
	// session get different temporary files instead of writing into one.
	var f *os.File
	var partPath string
	for i := 0; i < 1000; i++ {
		partPath = filepath.Join(r.dir, fmt.Sprintf("%s.%s.%d.part", clean, sanitizeFileName(id), i))
		var err error
		f, err = os.OpenFile(partPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("open %s: %w", partPath, err)
		}
	}
	if f == nil {
		return errors.New("could not claim a temporary file name")
	}
	r.transfers[id] = &incomingFile{name: clean, partPath: partPath, f: f, size: size}
	return nil
}

// chunk appends one base64 chunk. Chunks must arrive in order; the relay keeps
// order on one socket, so a gap means a chunk was lost and the file is wrong.
func (r *fileReceiver) chunk(id string, seq int, data string) (received int64, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.transfers[id]
	if !ok {
		return 0, fmt.Errorf("no transfer %q in progress", id)
	}
	fail := func(err error) (int64, error) {
		r.abortLocked(id)
		return 0, err
	}
	if seq != t.nextSeq {
		return fail(fmt.Errorf("chunk %d arrived, expected %d", seq, t.nextSeq))
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fail(fmt.Errorf("chunk %d is not valid base64: %w", seq, err))
	}
	if t.received+int64(len(raw)) > t.size {
		return fail(fmt.Errorf("received more than the declared %d bytes", t.size))
	}
	if _, err := t.f.Write(raw); err != nil {
		return fail(fmt.Errorf("write: %w", err))
	}
	t.received += int64(len(raw))
	t.nextSeq++
	return t.received, nil
}

// end closes a transfer and moves it to its final, non-colliding name.
func (r *fileReceiver) end(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.transfers[id]
	if !ok {
		return "", fmt.Errorf("no transfer %q in progress", id)
	}
	if t.received != t.size {
		r.abortLocked(id)
		return "", fmt.Errorf("received %d of %d bytes", t.received, t.size)
	}
	if err := t.f.Close(); err != nil {
		r.abortLocked(id)
		return "", fmt.Errorf("close: %w", err)
	}
	delete(r.transfers, id)

	ext := filepath.Ext(t.name)
	stem := strings.TrimSuffix(t.name, ext)
	for i := 0; i < 1000; i++ {
		candidate := t.name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		final := filepath.Join(r.dir, candidate)
		// Link-then-remove is an atomic "rename unless it exists": os.Rename
		// replaces an existing file on every platform we build for.
		if err := os.Link(t.partPath, final); err == nil {
			_ = os.Remove(t.partPath)
			return final, nil
		} else if !errors.Is(err, os.ErrExist) {
			// A filesystem without hard links (FAT, some network shares):
			// fall back to an existence check, which is racy only against
			// another writer choosing the same name in the same instant.
			if _, statErr := os.Stat(final); errors.Is(statErr, os.ErrNotExist) {
				if renameErr := os.Rename(t.partPath, final); renameErr == nil {
					return final, nil
				}
			}
		}
	}
	_ = os.Remove(t.partPath)
	return "", errors.New("could not find a free file name")
}

// abort discards a transfer the console gave up on.
func (r *fileReceiver) abort(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.abortLocked(id)
}

func (r *fileReceiver) abortLocked(id string) {
	t, ok := r.transfers[id]
	if !ok {
		return
	}
	_ = t.f.Close()
	_ = os.Remove(t.partPath)
	delete(r.transfers, id)
}

// closeAll drops every unfinished upload when the session ends, so a session
// that stops mid-transfer leaves no .part files behind.
func (r *fileReceiver) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.transfers {
		r.abortLocked(id)
	}
}

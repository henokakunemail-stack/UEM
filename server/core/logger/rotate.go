package logger

import (
	"fmt"
	"os"
	"sync"
)

// rotatingFile is LOG_FILE with a size limit.
//
// The file used to be appended to forever: a busy server's log grew until the
// disk filled, and a full disk takes the SQLite database down with it. When the
// file passes maxBytes it is renamed to <file>.1 (older ones shift up to
// <file>.<keep>, the oldest is deleted) and a fresh file is started.
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	keep     int
	f        *os.File
	size     int64
}

func openRotating(path string, maxBytes int64, keep int) (*rotatingFile, error) {
	r := &rotatingFile{path: path, maxBytes: maxBytes, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, fi.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxBytes > 0 && r.size+int64(len(p)) > r.maxBytes && r.size > 0 {
		if err := r.rotate(); err != nil {
			// Keep logging to the current file rather than losing lines; a
			// rotation that failed (a locked file on Windows) is retried on
			// the next write.
			fmt.Fprintf(os.Stderr, "log rotation failed: %v\n", err)
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	if r.keep > 0 {
		_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
		for i := r.keep - 1; i >= 1; i-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
		}
		if err := os.Rename(r.path, r.path+".1"); err != nil {
			_ = r.open()
			return err
		}
	} else if err := os.Truncate(r.path, 0); err != nil {
		_ = r.open()
		return err
	}
	return r.open()
}

// file is the open handle, for reading the tail back at startup.
func (r *rotatingFile) file() *os.File {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f
}

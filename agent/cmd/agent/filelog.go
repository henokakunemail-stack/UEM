package main

import (
	"io"
	"os"
	"path/filepath"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// maxLogBytes is the size at which a log file is rotated to <name>.log.1 on
// the next start, so a service that runs for months cannot fill the disk.
const maxLogBytes = 10 << 20

// setupFileLog sends the agent's log to <dir>/logs/<name>.log as well as
// stderr, and returns the file so it can be closed.
//
// The Windows service and the remote control worker have no stderr anyone can
// read: the SCM discards it and the worker is a detached process with no
// console. Every warning they wrote -- including why a remote control session
// could not take input -- went nowhere. A failure to open the file leaves
// logging on stderr only; it never stops the agent.
func setupFileLog(dir, name string) *os.File {
	if dir == "" {
		return nil
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil
	}
	path := filepath.Join(logDir, name+".log")
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	log.Logger = zerolog.New(io.MultiWriter(os.Stderr, f)).With().Timestamp().Logger()
	return f
}

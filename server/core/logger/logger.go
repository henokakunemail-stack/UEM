package logger

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// tail is a bounded ring of the most recent formatted log lines. The console's
// "Log" menu reads it via GET /api/logs. Bounded on purpose: an endpoint
// manager runs for months, and an unbounded slice would be a slow memory leak.
var tail = struct {
	sync.RWMutex
	lines []string
}{}

const maxTailLines = 2000

func remember(line string) {
	tail.Lock()
	defer tail.Unlock()
	if len(tail.lines) >= maxTailLines {
		// Shift by half instead of one: O(1) amortized, and the buffer only
		// needs to be a rolling window, not an exact ring.
		tail.lines = append(tail.lines[:0], tail.lines[len(tail.lines)/2:]...)
	}
	tail.lines = append(tail.lines, line)
}

// Tail returns a copy of the buffered log lines, oldest first.
func Tail() []string {
	tail.RLock()
	defer tail.RUnlock()
	out := make([]string, len(tail.lines))
	copy(out, tail.lines)
	return out
}

// Init configures the global zerolog logger.
// level: debug|info|warn|error
// file: optional path; when non-empty, logs are appended to it as well and that
// file is the source the Log menu tails.
func Init(level string, file string) {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(lvl)

	var sinks []io.Writer
	// Human-readable console output, always: this is what an operator tails
	// under systemd or docker.
	sinks = append(sinks, zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	if file != "" {
		if err := os.MkdirAll(filepath.Dir(file), 0755); err == nil {
			f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				log.Warn().Err(err).Str("path", file).
					Msg("LOG_FILE could not be opened; continuing with stdout only")
			} else {
				// A restart must not silently start a fresh log and lose the
				// incident that led to it, so this is append-only, never truncate.
				sinks = append(sinks, f)
				rememberTailFrom(f, maxTailLines)
			}
		} else {
			log.Warn().Err(err).Str("path", file).Msg("could not create log directory")
		}
	}

	// Console stays human-readable; the file and the in-memory tail get the raw
	// JSON event, so the Log menu can color by level and grep by timestamp.
	w := zerolog.MultiLevelWriter(append(sinks, &tailWriter{})...)
	log.Logger = zerolog.New(w).With().Timestamp().Caller().Logger()
}

// tailWriter funnels structured log events into the in-memory ring as
// human-readable single lines, so the Log menu shows the same text an operator
// would see on the terminal.
type tailWriter struct{}

func (tailWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	if line != "" {
		remember(line)
	}
	return len(p), nil
}

// rememberTailFrom pre-loads the ring with the tail of an existing log file so
// the Log menu is useful immediately after a restart rather than blank until
// the next event.
func rememberTailFrom(f *os.File, n int) {
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return
	}
	// Read at most the last 256 KB; anything older belongs in the file anyway.
	const maxBytes = 256 * 1024
	readBytes := fi.Size()
	if readBytes > maxBytes {
		readBytes = maxBytes
	}
	if _, err := f.Seek(-readBytes, io.SeekEnd); err != nil {
		return
	}
	buf := make([]byte, readBytes)
	nread, _ := io.ReadFull(f, buf)
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return // appends would overwrite; give up preloading rather than corrupt
	}

	var lines []string
	for _, ln := range strings.Split(string(buf[:nread]), "\n") {
		if strings.TrimSpace(ln) != "" {
			lines = append(lines, ln)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	tail.Lock()
	tail.lines = lines
	tail.Unlock()
}

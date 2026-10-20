//go:build linux || darwin

package remoteexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"syscall"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/unix"
)

// PTYSupported reports whether StartPTYSession gives a real terminal here.
const PTYSupported = true

// StartPTYSession runs the shell on a pseudo-terminal, which is what makes the
// console terminal behave like PuTTY: the shell prints its prompt and echoes
// keystrokes itself, Ctrl+C reaches the foreground job as SIGINT, and
// full-screen programs (vim, top, less) get a screen size to draw into. The
// pipe-based StartSession gave none of that -- bash on a pipe is not
// interactive in any useful sense.
func (m *TerminalManager) StartPTYSession(
	sessionID, shell string,
	cols, rows uint16,
	onOutput func(data string),
	onClose func(),
) error {
	m.mu.Lock()
	if _, exists := m.sessions[sessionID]; exists {
		m.mu.Unlock()
		return errors.New("terminal session already exists")
	}

	master, slave, err := openPTY()
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if cols > 0 && rows > 0 {
		_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: cols, Row: rows})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := ptyShellCmd(ctx, shell)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// A session of its own with the pty as controlling terminal: job control
	// works, and closing the master hangs up everything started from it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}

	if err := cmd.Start(); err != nil {
		_ = master.Close()
		_ = slave.Close()
		cancel()
		m.mu.Unlock()
		return err
	}
	// The child holds its own copy; keeping ours open would stop the master
	// from ever reading EOF after the shell exits.
	_ = slave.Close()

	sess := &ProcessSession{
		SessionID: sessionID,
		Cmd:       cmd,
		Stdin:     master,
		Cancel:    cancel,
		resize: func(c, r uint16) error {
			return unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: c, Row: r})
		},
	}
	m.sessions[sessionID] = sess
	m.mu.Unlock()

	log.Info().Str("session_id", sessionID).Str("shell", cmd.Path).Msg("started pty terminal session")

	go func() {
		var carry utf8Carry
		buf := make([]byte, 16<<10)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				if s := carry.feed(buf[:n]); s != "" {
					onOutput(s)
				}
			}
			if err != nil {
				// EIO is how Linux reports "the other side hung up" on a master.
				break
			}
		}
		if s := carry.flush(); s != "" {
			onOutput(s)
		}
	}()

	go func() {
		_ = cmd.Wait()
		_ = m.CloseSession(sessionID)
		if onClose != nil {
			onClose()
		}
		log.Info().Str("session_id", sessionID).Msg("pty terminal session ended")
	}()

	return nil
}

// ptyShellCmd builds a login shell for the operator: the account's home as
// working directory and an environment a terminal program expects. Under
// systemd or launchd the agent's own environment has no TERM and often no
// HOME, and a shell started with that draws nothing in colour and cannot find
// its rc files.
func ptyShellCmd(ctx context.Context, shell string) *exec.Cmd {
	path := shellPath(shell)
	cmd := exec.CommandContext(ctx, path, "-l")

	home := os.Getenv("HOME")
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		home, name = u.HomeDir, u.Username
	}
	if home == "" {
		home = "/"
	}
	cmd.Dir = home

	env := []string{
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"HOME=" + home,
		"USER=" + name,
		"LOGNAME=" + name,
		"SHELL=" + path,
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "TERM", "COLORTERM", "HOME", "USER", "LOGNAME", "SHELL":
		default:
			env = append(env, kv)
		}
	}
	if os.Getenv("PATH") == "" {
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	if os.Getenv("LANG") == "" {
		env = append(env, "LANG=C.UTF-8")
	}
	cmd.Env = env
	return cmd
}

// shellPath maps the console's shell name to a binary that exists, falling
// back to /bin/sh rather than failing on a minimal image without bash.
func shellPath(shell string) string {
	var candidates []string
	switch strings.ToLower(shell) {
	case "zsh":
		candidates = []string{"/bin/zsh", "/usr/bin/zsh", "/bin/bash"}
	case "sh":
		candidates = []string{"/bin/sh"}
	default:
		candidates = []string{"/bin/bash", "/usr/bin/bash", "/bin/zsh"}
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return "/bin/sh"
}

package remoteexec

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"

	"github.com/rs/zerolog/log"
)

type ProcessSession struct {
	SessionID string
	Cmd       *exec.Cmd
	Stdin     io.WriteCloser
	Cancel    context.CancelFunc
	closed    bool
	mu        sync.Mutex
	// resize is set only for pty sessions; a pipe has no window size.
	resize func(cols, rows uint16) error
}

// Resize sets a pty session's window size, which the shell and full-screen
// programs learn through SIGWINCH.
func (m *TerminalManager) Resize(sessionID string, cols, rows uint16) error {
	m.mu.RLock()
	sess, exists := m.sessions[sessionID]
	m.mu.RUnlock()
	if !exists {
		return errors.New("terminal session not found")
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.closed {
		return errors.New("terminal session is closed")
	}
	if sess.resize == nil {
		return errors.New("terminal session has no window size")
	}
	if cols == 0 || rows == 0 {
		return errors.New("window size must be positive")
	}
	return sess.resize(cols, rows)
}

type TerminalManager struct {
	mu       sync.RWMutex
	sessions map[string]*ProcessSession
}

func NewTerminalManager() *TerminalManager {
	return &TerminalManager{
		sessions: make(map[string]*ProcessSession),
	}
}

func (m *TerminalManager) StartSession(
	sessionID, shell string,
	onOutput func(data string),
	onClose func(),
) error {
	m.mu.Lock()
	if _, exists := m.sessions[sessionID]; exists {
		m.mu.Unlock()
		return errors.New("terminal session already exists")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := createShellCmd(ctx, shell)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		m.mu.Unlock()
		return err
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		cancel()
		m.mu.Unlock()
		return err
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		cancel()
		m.mu.Unlock()
		return err
	}

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		cancel()
		m.mu.Unlock()
		return err
	}

	sess := &ProcessSession{
		SessionID: sessionID,
		Cmd:       cmd,
		Stdin:     stdinPipe,
		Cancel:    cancel,
	}
	m.sessions[sessionID] = sess
	m.mu.Unlock()

	log.Info().Str("session_id", sessionID).Str("shell", shell).Msg("started interactive terminal shell process")

	// Read stdout stream
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				onOutput(string(buf[:n]))
			}
			if err != nil {
				break
			}
		}
	}()

	// Read stderr stream
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := stderrPipe.Read(buf)
			if n > 0 {
				onOutput(string(buf[:n]))
			}
			if err != nil {
				break
			}
		}
	}()

	// Wait for process termination
	go func() {
		_ = cmd.Wait()
		_ = m.CloseSession(sessionID)
		if onClose != nil {
			onClose()
		}
		log.Info().Str("session_id", sessionID).Msg("interactive terminal shell process ended")
	}()

	return nil
}

func (m *TerminalManager) WriteInput(sessionID string, data string) error {
	m.mu.RLock()
	sess, exists := m.sessions[sessionID]
	m.mu.RUnlock()

	if !exists {
		return errors.New("terminal session not found")
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.closed || sess.Stdin == nil {
		return errors.New("terminal session is closed")
	}

	_, err := io.WriteString(sess.Stdin, data)
	return err
}

func (m *TerminalManager) CloseSession(sessionID string) error {
	m.mu.Lock()
	sess, exists := m.sessions[sessionID]
	if !exists {
		m.mu.Unlock()
		return nil
	}
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.closed {
		return nil
	}
	sess.closed = true

	if sess.Stdin != nil {
		_ = sess.Stdin.Close()
	}
	if sess.Cancel != nil {
		sess.Cancel()
	}
	if sess.Cmd != nil && sess.Cmd.Process != nil {
		_ = sess.Cmd.Process.Kill()
	}

	return nil
}

func (m *TerminalManager) CloseAll() {
	m.mu.Lock()
	sessions := make([]*ProcessSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[string]*ProcessSession)
	m.mu.Unlock()

	for _, s := range sessions {
		s.mu.Lock()
		if !s.closed {
			s.closed = true
			if s.Stdin != nil {
				_ = s.Stdin.Close()
			}
			if s.Cancel != nil {
				s.Cancel()
			}
			if s.Cmd != nil && s.Cmd.Process != nil {
				_ = s.Cmd.Process.Kill()
			}
		}
		s.mu.Unlock()
	}
}

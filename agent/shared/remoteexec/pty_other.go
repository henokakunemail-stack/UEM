//go:build !linux && !darwin

package remoteexec

import "errors"

// PTYSupported is false here: Windows keeps the pipe-based StartSession.
const PTYSupported = false

func (m *TerminalManager) StartPTYSession(
	sessionID, shell string,
	cols, rows uint16,
	onOutput func(data string),
	onClose func(),
) error {
	return errors.New("pty terminals are not supported on this platform")
}

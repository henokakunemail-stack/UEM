package remotecontrol

import "errors"

// unavailableCapturer stands in when the session cannot reach a desktop at all,
// for instance when a Windows service has no logged-on user to hand the
// session to. It reports why through Capabilities, so the console shows the
// reason instead of a session that never draws -- or worse, the service's own
// invisible Session 0 desktop, which a blit can succeed on and input can be
// sent to without anyone ever seeing it.
type unavailableCapturer struct{ reason string }

// NewUnavailableCapturer returns a capturer that only explains itself.
func NewUnavailableCapturer(reason string) ScreenCapturer {
	return unavailableCapturer{reason: reason}
}

func (u unavailableCapturer) Capabilities() Capabilities {
	return Capabilities{Reason: u.reason}
}

func (u unavailableCapturer) CaptureScreen() ([]byte, int, int, error) {
	return nil, 0, 0, errors.New(u.reason)
}

func (u unavailableCapturer) InjectMouseEvent(InputEvent) error    { return errors.New(u.reason) }
func (u unavailableCapturer) InjectKeyboardEvent(InputEvent) error { return errors.New(u.reason) }
func (u unavailableCapturer) ReleaseAllKeys() error                { return nil }

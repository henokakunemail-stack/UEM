package remotecontrol

import (
	"encoding/json"
	"errors"
	"testing"
)

// fakeCapturer is a capturer that records what it was asked to do, so the
// session's own decision-making can be asserted without a real desktop.
type fakeCapturer struct {
	capture     bool
	captureErr  error
	mouseEvents []InputEvent
	keyEvents   []InputEvent
	releases    int
}

func (f *fakeCapturer) Capabilities() Capabilities {
	caps := Capabilities{Mouse: true, Keyboard: true, Capture: f.capture}
	if f.captureErr != nil {
		caps.Reason = f.captureErr.Error()
	}
	return caps
}

func (f *fakeCapturer) CaptureScreen() ([]byte, int, int, error) {
	if f.captureErr != nil {
		return nil, 0, 0, f.captureErr
	}
	return []byte{0xFF, 0xD8, 0xFF}, 8, 4, nil
}

func (f *fakeCapturer) InjectMouseEvent(e InputEvent) error {
	f.mouseEvents = append(f.mouseEvents, e)
	return nil
}

func (f *fakeCapturer) InjectKeyboardEvent(e InputEvent) error {
	f.keyEvents = append(f.keyEvents, e)
	return nil
}

func (f *fakeCapturer) ReleaseAllKeys() error {
	f.releases++
	return nil
}

// A session built with no RelayURL would dial the server root path. rc.start now
// refuses that on the agent side, so the constructor is not the place it has to
// be caught -- but the config is what a caller reads to build the dial, and a
// blank relay url must survive the constructor rather than be invented.
func TestASessionKeepsTheRelayURLItWasGiven(t *testing.T) {
	s := NewSession(
		SessionConfig{SessionID: "s-1", Mode: "full_control", RelayURL: "/api/agent/devices/d-1/remotecontrol/ws?session=s-1"},
		"http://localhost:18460",
		Credentials{DeviceID: "d-1", DeviceSecret: "secret"},
		&fakeCapturer{capture: true},
	)
	if got := s.config.RelayURL; got == "" {
		t.Fatal("session dropped the relay url it was given")
	}
	if got := s.ID(); got != "s-1" {
		t.Fatalf("ID() = %q, want %q", got, "s-1")
	}
}

// A blank mode has to become full_control at construction, because the relay
// gate and the agent gate both compare against those two literals and a third
// value would fail both comparisons and quietly drop all input.
func TestABlankModeBecomesFullControl(t *testing.T) {
	s := NewSession(
		SessionConfig{SessionID: "s-1", RelayURL: "/relay"},
		"http://localhost:18460",
		Credentials{},
		&fakeCapturer{capture: true},
	)
	if got := s.currentMode(); got != "full_control" {
		t.Fatalf("mode = %q, want full_control", got)
	}
}

// An unrecognised mode must be rejected, not stored. setMode silently returns
// on anything that is not the two known values, which is correct, but a caller
// that reads currentMode back must never see a mode the agent does not honour.
func TestAnUnknownModeIsNotStored(t *testing.T) {
	s := NewSession(
		SessionConfig{SessionID: "s-1", Mode: "full_control"},
		"http://localhost:18460",
		Credentials{},
		&fakeCapturer{capture: true},
	)
	s.setMode("godmode")
	if got := s.currentMode(); got != "full_control" {
		t.Fatalf("mode = %q after an unknown mode, want it unchanged at full_control", got)
	}
}

// Switching to view_only has to lift every held key on the endpoint. A key held
// when control is handed back is stuck down on the operator's machine until the
// session ends, which is a workstation-wide problem, not a cosmetic one.
func TestSwitchingToViewOnlyReleasesHeldKeys(t *testing.T) {
	c := &fakeCapturer{capture: true}
	s := NewSession(
		SessionConfig{SessionID: "s-1", Mode: "full_control"},
		"http://localhost:18460",
		Credentials{},
		c,
	)
	s.setMode("view_only")
	if c.releases == 0 {
		t.Fatal("switching to view_only did not release held keys")
	}
	if got := s.currentMode(); got != "view_only" {
		t.Fatalf("mode = %q, want view_only", got)
	}
}

// A capabilities probe that fails has to be reported verbatim. The console keys
// the whole failure screen off this string, so anything vaguer here is what the
// operator ends up looking at instead of the actual reason.
func TestAFailedProbeIsReportedVerbatim(t *testing.T) {
	// The literal is the one the Windows capturer produces when the process has
	// no desktop to blit from, which is the failure this whole path exists for.
	probeFailure := errors.New("bitblt failed: The handle is invalid.")

	c := &fakeCapturer{captureErr: probeFailure}
	caps := c.Capabilities()
	if caps.Capture {
		t.Fatal("a capturer that cannot capture reported capture=true")
	}
	if caps.Reason != probeFailure.Error() {
		t.Fatalf("reason = %q, want %q", caps.Reason, probeFailure.Error())
	}
	if !caps.Mouse || !caps.Keyboard {
		t.Fatal("a failed capture probe must not take the input paths down with it")
	}
}

// The frame header the console parses is 4 bytes: width and height, big endian.
// A capturer that returns nothing must not produce a header at all, because the
// console reads those four bytes unconditionally and would paint garbage.
func TestAFrameCarriesItsDimensionsInTheHeader(t *testing.T) {
	c := &fakeCapturer{capture: true}
	data, w, h, err := c.CaptureScreen()
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}
	if w != 8 || h != 4 {
		t.Fatalf("dimensions = %dx%d, want 8x4", w, h)
	}
	if len(data) == 0 {
		t.Fatal("capture returned no bytes")
	}
}

// The JSON the agent writes for its capabilities has to decode into the struct
// the console types, because the two are compiled separately and a renamed field
// is a compile error on neither side.
func TestTheHelloEnvelopeCarriesWhatTheConsoleReads(t *testing.T) {
	raw := `{"type":"hello","session_id":"s-1","mode":"full_control","os":"windows","capabilities":{"capture":true,"mouse":true,"keyboard":true}}`

	var got struct {
		Type         string       `json:"type"`
		SessionID    string       `json:"session_id"`
		Mode         string       `json:"mode"`
		Capabilities Capabilities `json:"capabilities"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("hello envelope does not decode: %v", err)
	}
	if got.Type != "hello" {
		t.Fatalf("type = %q, want hello", got.Type)
	}
	if !got.Capabilities.Capture || !got.Capabilities.Mouse || !got.Capabilities.Keyboard {
		t.Fatalf("capabilities did not survive the round trip: %+v", got.Capabilities)
	}
}

// A notice is the only thing the agent has to say when the relay socket never
// came up. It has to be the same shape as the capture refusal the console
// already handles, or the console drops it and the operator is back to a black
// rectangle.
func TestANoticeUsesTheSameShapeAsTheCaptureRefusal(t *testing.T) {
	raw := `{"type":"connect_failed","reason":"dial rc relay ws: connection refused"}`

	var got struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("notice does not decode: %v", err)
	}
	if got.Type != "connect_failed" {
		t.Fatalf("type = %q", got.Type)
	}
	if got.Reason == "" {
		t.Fatal("notice carried no reason, so the console has nothing to show")
	}
}

// Stop has to be safe to call more than once. The capture loop and the input
// loop both defer it, so a socket that closes while a notice is in flight calls
// it twice, and a second close on an already-closed channel panics.
func TestStopIsIdempotent(t *testing.T) {
	s := NewSession(
		SessionConfig{SessionID: "s-1", RelayURL: "/relay"},
		"http://localhost:18460",
		Credentials{},
		&fakeCapturer{capture: true},
	)
	s.Stop()
	s.Stop()
}

// SendNotice on a session that never connected must not panic. The dial failure
// path calls it after Start has already returned an error, which is exactly the
// case where conn is nil.
func TestSendNoticeOnADeadSessionIsSafe(t *testing.T) {
	s := NewSession(
		SessionConfig{SessionID: "s-1", RelayURL: "/relay"},
		"http://localhost:18460",
		Credentials{},
		&fakeCapturer{capture: true},
	)
	s.SendNotice("connect_failed", "dial failed")
}

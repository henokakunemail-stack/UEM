package remotecontrol

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The window: two frames may be unacknowledged, the third waits.
func TestTheWindowClosesAfterTwoUnacknowledgedFrames(t *testing.T) {
	var f flowControl
	now := time.Now()
	f.ack(0, now) // the console says it acknowledges frames
	f.written(100, now)
	if f.full() {
		t.Fatal("full after one frame")
	}
	f.written(100, now)
	if !f.full() {
		t.Fatal("not full after two unacknowledged frames")
	}
	f.ack(1, now.Add(200*time.Millisecond))
	if f.full() {
		t.Fatal("still full after the first frame was acknowledged")
	}
	d := f.drain()
	if len(d) != 1 || d[0].rtt != 200*time.Millisecond || d[0].bytes != 100 {
		t.Fatalf("deliveries = %+v, want one of 200ms and 100 bytes", d)
	}
}

// Frames written before the console's first acknowledgement were partly
// dropped by the relay, which forwards no frames until a console attaches, so
// the console's count starts below the agent's. Taken at face value, that
// difference would hold the window shut for the whole session.
func TestFramesSentBeforeTheConsoleAttachedDoNotHoldTheWindowShut(t *testing.T) {
	var f flowControl
	now := time.Now()
	for i := 0; i < 7; i++ {
		f.written(100, now)
	}
	f.ack(0, now)
	if f.full() {
		t.Fatal("the window is shut by frames from before the console attached")
	}
	f.written(100, now)
	f.written(100, now)
	if !f.full() {
		t.Fatal("two new frames did not fill the window")
	}
	// Two of the early frames were in transit and the console counts them
	// too; its count overtakes the agent's and the mapping corrects itself.
	f.ack(4, now)
	if f.full() {
		t.Fatal("still full after the console reported more frames than were sent")
	}
	f.written(100, now)
	f.written(100, now)
	if !f.full() {
		t.Fatal("after the correction, two new frames did not fill the window")
	}
	f.ack(5, now)
	if f.full() {
		t.Fatal("the next acknowledgement did not open the window")
	}
}

// A console that never acknowledges is an older console: nothing waits.
func TestWithoutAcknowledgementsThereIsNoWindow(t *testing.T) {
	var f flowControl
	for i := 0; i < 50; i++ {
		f.written(100, time.Now())
	}
	if f.full() || f.active() {
		t.Fatal("an older console's stream is being held back")
	}
}

// Delivery time is what moves the quality now: frames written to the server
// instantly can still take a second to reach an operator at home.
func TestSlowDeliveryLowersQualityEvenWhenWritesAreFast(t *testing.T) {
	q := newQualityController(time.Now())
	start := q.quality
	now := time.Now()
	for i := 0; i < 5; i++ {
		now = now.Add(600 * time.Millisecond)
		q.observeDelivery(150_000, 800*time.Millisecond, now)
	}
	if q.quality >= start {
		t.Fatalf("quality stayed at %d through 800ms deliveries", q.quality)
	}
}

// ackRelay connects a session to a fake relay and starts streaming a desktop
// that changes on every capture. It returns the relay end.
func ackRelay(t *testing.T, mode string, w, h int) (*Session, *websocket.Conn) {
	t.Helper()
	c := &movingCapturer{fakeCapturer: fakeCapturer{capture: true}, w: w, h: h, changes: 1 << 30}
	s := NewSession(SessionConfig{SessionID: "s-1", Mode: mode}, "http://localhost:1", Credentials{}, c)
	relay := relayPair(t, s)
	go s.readInputLoop()
	go s.streamFramesLoop()
	t.Cleanup(s.Stop)
	return s, relay
}

func sendInput(t *testing.T, relay *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := relay.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatal(err)
	}
}

// frameLog reads every message the session sends, on one goroutine for the
// life of the test, and records when each frame arrived and its size. A read
// deadline cannot be used to sample the stream in slices: once one expires,
// gorilla/websocket fails every later read on that connection.
type frameLog struct {
	mu    sync.Mutex
	times []time.Time
	dims  [][2]int
}

func logFrames(relay *websocket.Conn) *frameLog {
	l := &frameLog{}
	go func() {
		for {
			kind, b, err := relay.ReadMessage()
			if err != nil {
				return
			}
			if kind != websocket.BinaryMessage {
				continue
			}
			_, w, h, _, _ := DecodeFrameHeader(b)
			l.mu.Lock()
			l.times = append(l.times, time.Now())
			l.dims = append(l.dims, [2]int{w, h})
			l.mu.Unlock()
		}
	}()
	return l
}

// since returns how many frames arrived after t, and the size of the last.
func (l *frameLog) since(t time.Time) (int, int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, w, h := 0, 0, 0
	for i, at := range l.times {
		if at.After(t) {
			n++
			w, h = l.dims[i][0], l.dims[i][1]
		}
	}
	return n, w, h
}

// A console that acknowledges and then stops (its link is saturated, or the
// tab is in the background) receives no more than the window. Without the
// window the agent kept sending into a queue that grew for seconds.
func TestAConsoleThatStopsAcknowledgingStopsTheStream(t *testing.T) {
	_, relay := ackRelay(t, "full_control", 320, 200)
	frames := logFrames(relay)
	time.Sleep(300 * time.Millisecond)
	if n, _, _ := frames.since(time.Time{}); n < 3 {
		t.Fatalf("only %d frames before any acknowledgement; the fixture is not streaming", n)
	}
	sendInput(t, relay, InputEvent{Type: "ack", Frames: 0})
	time.Sleep(300 * time.Millisecond) // frames already in flight land
	mark := time.Now()
	time.Sleep(time.Second)
	if n, _, _ := frames.since(mark); n > ackWindow {
		t.Fatalf("%d frames arrived in a second with nothing acknowledged; the window is %d", n, ackWindow)
	}
	mark = time.Now()
	sendInput(t, relay, InputEvent{Type: "ack", Frames: 1 << 20})
	time.Sleep(500 * time.Millisecond)
	if n, _, _ := frames.since(mark); n == 0 {
		t.Fatal("acknowledging the frames did not resume the stream")
	}
}

// An older console never acknowledges, and its stream must not change.
func TestAnOlderConsoleStillReceivesAStream(t *testing.T) {
	_, relay := ackRelay(t, "full_control", 320, 200)
	frames := logFrames(relay)
	time.Sleep(time.Second)
	if n, _, _ := frames.since(time.Time{}); n < 10 {
		t.Fatalf("only %d frames in a second to a console that sends no acknowledgements", n)
	}
}

// Acknowledgements are about delivery, not input, so view-only honours them:
// otherwise a view-only stream would stop after two frames.
func TestViewOnlyHonoursAcknowledgements(t *testing.T) {
	s, relay := ackRelay(t, "view_only", 320, 200)
	sendInput(t, relay, InputEvent{Type: "ack", Frames: 0})
	deadline := time.Now().Add(2 * time.Second)
	for !s.flow.active() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.flow.active() {
		t.Fatal("a view-only session ignored the console's acknowledgement")
	}
}

// Low-bandwidth mode sends half-size frames from the next frame on, without
// waiting for the stream to discover the link is slow, and caps the quality.
func TestLowBandwidthModeSendsHalfSizeFrames(t *testing.T) {
	s, relay := ackRelay(t, "full_control", 1920, 1080)
	frames := logFrames(relay)
	sendInput(t, relay, InputEvent{Type: "low_bandwidth", Enabled: true})
	time.Sleep(300 * time.Millisecond)
	mark := time.Now()
	time.Sleep(600 * time.Millisecond)
	if _, w, h := frames.since(mark); w != 960 || h != 540 {
		t.Fatalf("low-bandwidth frames are %dx%d, want 960x540", w, h)
	}
	if q := s.frameQuality(); q > lowBandwidthQuality {
		t.Fatalf("low-bandwidth quality is %d, want at most %d", q, lowBandwidthQuality)
	}
	sendInput(t, relay, InputEvent{Type: "low_bandwidth", Enabled: false})
	off := time.Now()
	deadline := off.Add(6 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if _, w, _ := frames.since(off); w == 1920 {
			return
		}
	}
	t.Fatal("turning low-bandwidth mode off never brought back full-size frames")
}

// The quality in the statistics is the one frames are encoded at. In
// low-bandwidth mode that is the capped value, not the adaptive one, or the
// toolbar shows Q80 for a stream sent at 50.
func TestStatsReportTheQualityActuallyUsed(t *testing.T) {
	s := NewSession(SessionConfig{SessionID: "s-1", Mode: "full_control"}, "http://localhost:1", Credentials{}, &fakeCapturer{capture: true})
	relay := relayPair(t, s)
	s.lowBandwidth.Store(true)
	s.maybeSendStats(time.Now())
	for _, m := range readControls(t, relay, 300*time.Millisecond) {
		if m["type"] == "stats" {
			if m["quality"] != float64(lowBandwidthQuality) || m["low_bandwidth"] != true {
				t.Fatalf("stats in low-bandwidth mode = %v, want quality %d", m, lowBandwidthQuality)
			}
			return
		}
	}
	t.Fatal("no stats message was sent")
}

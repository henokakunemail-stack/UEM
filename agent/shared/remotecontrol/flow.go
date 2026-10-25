package remotecontrol

import (
	"sync"
	"time"
)

// Flow control between the agent and the console.
//
// The agent used to judge the link by how long a socket write blocked. That is
// the right signal only when the agent's own socket is the narrow part of the
// path, and it is not when the operator works from outside the office: the
// agent writes to the server over the LAN, and the narrow part is the server's
// upload to the internet. Between the two sit the TCP buffers of both hops,
// which hold dozens of frames, so the agent kept sending full-size, high
// quality frames into a queue several seconds long before any write blocked.
// The operator saw a picture seconds behind the desktop that then lurched.
//
// A console that supports it acknowledges every frame once it has painted it.
// The agent keeps at most ackWindow frames unacknowledged, so the queue between
// them stays a frame or two deep, and it measures the time from writing a frame
// to its acknowledgement -- the real delivery time to the operator's screen --
// to choose quality and size. A console that sends no acknowledgements leaves
// the stream exactly as it was.

// ackWindow is how many frames may be on their way to the console at once. Two
// keeps the link busy (one frame in transit while the next is encoded) without
// letting a queue form.
const ackWindow = 2

const (
	// congestedRTT is a delivery time that means the path to the operator is
	// full: the picture is visibly behind the desktop.
	congestedRTT = 350 * time.Millisecond
	// calmRTT is a delivery time with room to spare for a sharper picture.
	calmRTT = 150 * time.Millisecond
)

// delivery is one acknowledged frame: how long it took to reach the console's
// canvas, and how big it was.
type delivery struct {
	rtt   time.Duration
	bytes int
}

// flowControl tracks frames written to the console and the acknowledgements
// that come back. Frames are counted, not named: the relay delivers in order,
// so "the console has painted n frames" identifies them.
type flowControl struct {
	mu      sync.Mutex
	enabled bool
	sent    int
	acked   int
	// offset maps the console's count to the agent's. Frames written before the
	// console attached were dropped by the relay and never counted there, so
	// the two counts differ by however many frames went out before the first
	// acknowledgement.
	offset int
	// times and sizes hold the write time and size of the frames not yet
	// acknowledged, indexed by frame number modulo their length.
	times   [16]time.Time
	sizes   [16]int
	pending []delivery
	lastRTT time.Duration
}

// written records a frame put on the wire.
func (f *flowControl) written(bytes int, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.times[f.sent%len(f.times)] = now
	f.sizes[f.sent%len(f.sizes)] = bytes
	f.sent++
}

// ack records that the console has painted `frames` frames in total.
func (f *flowControl) ack(frames int, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.enabled {
		// The first acknowledgement, which a supporting console sends as soon
		// as it is connected. Everything written before it is treated as gone:
		// the relay dropped frames sent before the console attached, and the
		// few in transit right now cannot be told apart from those.
		f.enabled = true
		f.offset = f.sent - frames
		f.acked = f.sent
		return
	}
	// Frames still in transit at the first acknowledgement are counted by the
	// console when they land, which carries its count past the agent's. The
	// offset is pulled back when that happens, so from then on the counts line
	// up exactly and the window stays ackWindow frames deep.
	if frames+f.offset > f.sent {
		f.offset = f.sent - frames
	}
	target := frames + f.offset
	for f.acked < target {
		i := f.acked % len(f.times)
		if f.sent-f.acked <= len(f.times) && !f.times[i].IsZero() {
			rtt := now.Sub(f.times[i])
			f.pending = append(f.pending, delivery{rtt: rtt, bytes: f.sizes[i]})
			f.lastRTT = rtt
		}
		f.acked++
	}
}

// full reports whether the window is used up: the next frame has to wait for
// an acknowledgement.
func (f *flowControl) full() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled && f.sent-f.acked >= ackWindow
}

// active reports whether the console acknowledges frames.
func (f *flowControl) active() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

// drain returns the deliveries measured since the last call.
func (f *flowControl) drain() []delivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.pending
	f.pending = nil
	return out
}

// rtt is the most recent delivery time, for the console's statistics.
func (f *flowControl) rtt() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastRTT
}

// Low-bandwidth mode is the operator saying the link is slow before the
// stream has had to discover it: half-size frames at a modest quality from the
// first frame, and no step back to full size when the desktop goes still.
const (
	lowBandwidthLevel   = 2 // index into scaleLevels: 1/2
	lowBandwidthQuality = 50
)

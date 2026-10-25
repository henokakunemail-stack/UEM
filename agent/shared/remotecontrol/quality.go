package remotecontrol

import "time"

// The adaptive range. Below 35 text stops being readable, which defeats the
// point of a remote session; above 80 the bytes grow much faster than anything
// an operator can see.
const (
	minQuality = 35
	maxQuality = 80

	// A write that blocks this long means the socket's send buffer is full: the
	// link is carrying less than the agent is producing, and every frame queued
	// behind this one arrives later than the last.
	congestedWrite = 120 * time.Millisecond
	// Writes this fast across a whole window mean the link is idle between
	// frames, so a sharper picture costs nothing the operator would feel.
	idleWrite = 30 * time.Millisecond

	stepDownEvery = 500 * time.Millisecond
	stepUpAfter   = 3 * time.Second
)

// qualityController picks the JPEG quality from how long frame writes block.
//
// Write duration is the signal rather than throughput. A small frame lands in
// the kernel's send buffer instantly whatever the link speed, so bytes/second
// measured at the write reads as gigabits on a dial-up link. A write only blocks
// when the buffer is full, which is exactly the moment the link is the limit.
//
// Down is fast and up is slow on purpose: a congested link makes every second
// of delay visible to the operator, while a picture that stays slightly soft for
// three seconds longer than it had to is not.
type qualityController struct {
	quality   int
	lastDown  time.Time
	calmSince time.Time

	// windowBytes and windowStart produce the kbps figure the console shows.
	windowBytes int
	windowStart time.Time
	kbps        int
}

func newQualityController(now time.Time) *qualityController {
	return &qualityController{quality: defaultQuality, calmSince: now, windowStart: now}
}

// observe records one frame write and reports whether the quality changed.
func (q *qualityController) observe(bytes int, took time.Duration, now time.Time) bool {
	return q.observeSignal(bytes, took >= congestedWrite, took >= idleWrite, now)
}

// observeDelivery records one frame's delivery time to the console, the time
// from writing it to the console acknowledging it was painted. That is the
// signal when the console acknowledges frames: it covers the whole path to the
// operator, where a write to the server covers only the first hop.
func (q *qualityController) observeDelivery(bytes int, rtt time.Duration, now time.Time) bool {
	return q.observeSignal(bytes, rtt >= congestedRTT, rtt > calmRTT, now)
}

// observeSignal adjusts the quality from whether the path was congested, or
// merely not idle, for one frame.
func (q *qualityController) observeSignal(bytes int, congested, busy bool, now time.Time) bool {
	q.windowBytes += bytes
	if elapsed := now.Sub(q.windowStart); elapsed >= time.Second {
		q.kbps = int(float64(q.windowBytes*8) / 1000 / elapsed.Seconds())
		q.windowBytes = 0
		q.windowStart = now
	}

	switch {
	case congested:
		q.calmSince = now
		if q.quality > minQuality && now.Sub(q.lastDown) >= stepDownEvery {
			q.quality = max(q.quality-10, minQuality)
			q.lastDown = now
			return true
		}
	case busy:
		// Neither congested nor idle: hold, and restart the calm window.
		q.calmSince = now
	default:
		if q.quality < maxQuality && now.Sub(q.calmSince) >= stepUpAfter {
			q.quality = min(q.quality+5, maxQuality)
			q.calmSince = now
			return true
		}
	}
	return false
}

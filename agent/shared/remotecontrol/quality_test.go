package remotecontrol

import (
	"testing"
	"time"
)

// A link that keeps blocking writes has to get cheaper frames, and quickly: every
// congested frame queues the next one behind it.
func TestQualityStepsDownOnACongestedLink(t *testing.T) {
	now := time.Unix(0, 0)
	q := newQualityController(now)
	for i := 0; i < 20; i++ {
		now = now.Add(600 * time.Millisecond)
		q.observe(300_000, 400*time.Millisecond, now)
	}
	if q.quality != minQuality {
		t.Fatalf("after sustained congestion quality is %d, want the floor %d", q.quality, minQuality)
	}
}

// One slow write is not a trend. Stepping down on every blocked write would
// halve the quality in a second over one hiccup.
func TestQualityDoesNotStepDownTwiceInsideTheCooldown(t *testing.T) {
	now := time.Unix(0, 0)
	q := newQualityController(now)
	q.observe(1, 400*time.Millisecond, now.Add(time.Second))
	q.observe(1, 400*time.Millisecond, now.Add(time.Second+100*time.Millisecond))
	if q.quality != defaultQuality-10 {
		t.Fatalf("quality %d, want one step down to %d", q.quality, defaultQuality-10)
	}
}

// A link that recovered has to get its picture back, but only after it has been
// calm long enough to trust.
func TestQualityRecoversSlowlyWhenTheLinkIsIdle(t *testing.T) {
	now := time.Unix(0, 0)
	q := newQualityController(now)
	q.quality = minQuality

	now = now.Add(time.Second)
	if q.observe(1000, time.Millisecond, now) {
		t.Fatal("quality rose before the link had been calm for the full window")
	}
	for i := 0; i < 400; i++ {
		now = now.Add(100 * time.Millisecond)
		q.observe(1000, time.Millisecond, now)
	}
	if q.quality != maxQuality {
		t.Fatalf("after 40s idle quality is %d, want the ceiling %d", q.quality, maxQuality)
	}
}

func TestQualityReportsKbpsOverOneSecond(t *testing.T) {
	now := time.Unix(0, 0)
	q := newQualityController(now)
	q.observe(62_500, time.Millisecond, now.Add(500*time.Millisecond))
	q.observe(62_500, time.Millisecond, now.Add(time.Second))
	if q.kbps != 1000 {
		t.Fatalf("125000 bytes in 1s reported as %d kbps, want 1000", q.kbps)
	}
}

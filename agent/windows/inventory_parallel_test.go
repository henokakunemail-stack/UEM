//go:build windows

package windows

import (
	"context"
	"testing"
	"time"
)

// The posture script takes seconds of CIM queries, and it used to start only
// after software, OS detail and hardware were read, so a collection took the
// sum of both: about 16s, which every on-demand "Collect" in the console waited
// out. It must start with the rest, so a collection takes the longer of the two.
func TestPostureIsCollectedAlongsideTheRestOfTheInventory(t *testing.T) {
	const postureTakes = 3 * time.Second
	var postureStarted time.Time
	orig := runPostureScript
	runPostureScript = func(ctx context.Context) ([]byte, error) {
		postureStarted = time.Now()
		time.Sleep(postureTakes)
		return []byte(`{"bios_date":"2021-03-04"}`), nil
	}
	defer func() { runPostureScript = orig }()

	start := time.Now()
	rep, err := NewCollector().Collect()
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Posture == nil || rep.Posture.BIOSReleaseDate != "2021-03-04" {
		t.Fatalf("posture missing from the report: %+v", rep.Posture)
	}
	if len(rep.Software) == 0 {
		t.Fatal("software missing from the report")
	}
	startedAfter := postureStarted.Sub(start)
	t.Logf("collect took %v; posture started %v after the collection began", took, startedAfter)
	if startedAfter > 500*time.Millisecond {
		t.Errorf("posture started %v into the collection; it must run alongside the rest, not after it", startedAfter)
	}
}

//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// Runs the real collector on this machine. Opt-in: it starts PowerShell and
// reads live WMI, so it is for a developer checking the shape by hand.
func TestCollectPostureLive(t *testing.T) {
	if os.Getenv("POSTURE_LIVE") == "" {
		t.Skip("set POSTURE_LIVE=1 to run against this machine")
	}
	raw, err := runPostureScript(context.Background())
	t.Logf("script err=%v\nraw=%s", err, raw)
	p := CollectPosture(context.Background())
	b, _ := json.MarshalIndent(p, "", "  ")
	t.Logf("posture=%s", b)
}

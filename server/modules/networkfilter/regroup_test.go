package networkfilter

import (
	"context"
	"testing"
)

// TestDevicesRegroupedRedispatches: a device that joined or left a group kept
// the filter rules of its old groups until it reconnected, because membership
// changes never reached this module. The observer must send the recompiled
// rules straight away.
func TestDevicesRegroupedRedispatches(t *testing.T) {
	f := newConnectFixture(t)
	f.h.DevicesRegrouped(context.Background(), []string{"dev-1"})

	if len(f.hub.sent) != 1 {
		t.Fatalf("%d messages sent after a group change, want 1", len(f.hub.sent))
	}
	assertFilterMessage(t, f.hub.sent[0], f.version, []string{"detik.com"})
}

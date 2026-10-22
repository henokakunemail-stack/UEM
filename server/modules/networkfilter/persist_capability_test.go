package networkfilter

import (
	"context"
	"testing"
)

// TestReconnectRepushesWithoutPersistCapability: an agent that predates
// filter.persist keeps nothing across a restart, so a whitelist it was enforcing
// is gone when it reconnects, while its row still reads synced at the current
// version. Such a device is sent the policy again.
func TestReconnectRepushesWithoutPersistCapability(t *testing.T) {
	f := newConnectFixture(t)
	ctx := context.Background()
	f.database.MustExec(`UPDATE devices SET capabilities = '["ping","inventory.collect"]' WHERE id = 'dev-1'`)
	if err := f.repo.RecordDeviceFilterState(ctx, &DeviceFilterState{
		DeviceID: "dev-1", PolicyVersion: f.version, Status: statusSynced, RulesApplied: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.h.SyncOnReconnect(ctx, "dev-1"); err != nil {
		t.Fatal(err)
	}
	if len(f.hub.sent) != 1 {
		t.Errorf("%d messages sent to an agent that lost its filter on restart, want 1", len(f.hub.sent))
	}
}

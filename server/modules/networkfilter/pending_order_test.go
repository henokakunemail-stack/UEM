package networkfilter

import (
	"context"
	"testing"
)

// reportingHub stands in for an agent fast enough to apply the rules and report
// before the dispatch call has returned.
type reportingHub struct {
	repo    *Repository
	version string
}

func (h *reportingHub) Online(string) bool { return true }
func (h *reportingHub) SendTo(id string, _ []byte) bool {
	_ = h.repo.RecordDeviceFilterState(context.Background(), &DeviceFilterState{
		DeviceID: id, PolicyVersion: h.version, Status: statusSynced, RulesApplied: 1,
	})
	return true
}

// TestFilterPendingWrittenBeforeSend: the pending row was written after the
// send, overwriting the agent's report, and the device read pending with the
// rules already enforced.
func TestFilterPendingWrittenBeforeSend(t *testing.T) {
	f := newConnectFixture(t)
	f.h.hub = &reportingHub{repo: f.repo, version: f.version}
	if err := f.h.dispatchDevice(context.Background(), "dev-1"); err != nil {
		t.Fatal(err)
	}
	s, err := f.repo.GetDeviceFilterState(context.Background(), "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != statusSynced {
		t.Errorf("status after a fast report = %q, want synced: the dispatch overwrote the report", s.Status)
	}
}

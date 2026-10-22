package systempolicy

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

// hookHub is online and runs onSend inside SendTo, which is where a fast agent
// applies the policy and reports before the dispatch call has returned.
type hookHub struct {
	sent   int
	onSend func()
}

func (h *hookHub) Online(string) bool { return true }
func (h *hookHub) SendTo(string, []byte) bool {
	h.sent++
	if h.onSend != nil {
		h.onSend()
	}
	return true
}

func statusEnv(t *testing.T) (*policyEnv, string) {
	t.Helper()
	e := newPolicyEnv(t)
	e.create(map[string]any{"name": "Fleet", "target_type": "all",
		"settings": map[string]any{"settings_version": 2, "usb_storage_mode": "block"}})
	c, _ := e.compile()
	return e, c.Version
}

func (e *policyEnv) report(body map[string]any) (int, string) {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/agent/devices/dev-1/system-policy/report", body)
}

func (e *policyEnv) state() *DeviceSystemPolicyState {
	e.t.Helper()
	s, err := e.repo.GetDeviceState(context.Background(), "dev-1")
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// TestPendingWrittenBeforeSend: the pending row used to be written after the
// send, so an agent that applied and reported in between was overwritten back
// to pending and the device read "awaiting" with the policy already in force.
func TestPendingWrittenBeforeSend(t *testing.T) {
	e, version := statusEnv(t)
	hub := &hookHub{}
	hub.onSend = func() {
		_ = e.repo.UpdateDeviceState(context.Background(), &DeviceSystemPolicyState{
			DeviceID: "dev-1", PolicyVersion: version, Status: stateSynced,
		})
	}
	h := NewHandler(e.repo, hub, nil, nopAudit{}, nil)
	if err := h.dispatchDevice(context.Background(), "dev-1"); err != nil {
		t.Fatal(err)
	}
	if s := e.state(); s == nil || s.Status != stateSynced {
		t.Fatalf("state after a fast report = %+v, want synced: the dispatch overwrote the agent's report", s)
	}
}

// TestStaleReportIgnored: a report for a version that has since been replaced
// must not mark the device as enforcing the current one.
func TestStaleReportIgnored(t *testing.T) {
	e, version := statusEnv(t)
	code, body := e.report(map[string]any{"policy_version": "superseded", "status": "synced"})
	if code != http.StatusOK || !strings.Contains(body, `"stale"`) {
		t.Fatalf("stale report -> %d %s, want 200 stale", code, body)
	}
	if s := e.state(); s != nil {
		t.Fatalf("a stale report was stored: %+v (current version %s)", s, version)
	}
}

// TestUnknownStatusRejected: an unrecognised status used to be stored as synced.
func TestUnknownStatusRejected(t *testing.T) {
	e, version := statusEnv(t)
	code, body := e.report(map[string]any{"policy_version": version, "status": "applied-mostly"})
	if code != http.StatusBadRequest {
		t.Fatalf("unknown status -> %d %s, want 400", code, body)
	}
	if s := e.state(); s != nil {
		t.Fatalf("an unknown status was stored as %q", s.Status)
	}
}

// TestLegacyReportWithoutResultsAccepted: agents already deployed send only a
// status, and must keep working.
func TestLegacyReportWithoutResultsAccepted(t *testing.T) {
	e, version := statusEnv(t)
	if code, body := e.report(map[string]any{"policy_version": version, "status": "failed",
		"error_message": "registry refused"}); code != http.StatusOK {
		t.Fatalf("legacy report -> %d %s", code, body)
	}
	if s := e.state(); s.Status != stateFailed || s.ErrorMessage != "registry refused" || len(s.Results) != 0 {
		t.Errorf("state = %+v", s)
	}
}

// TestResultsDerivePartial: per-setting results decide the status, so a Linux
// endpoint that cannot enforce Windows settings reads "partial", not synced.
func TestResultsDerivePartial(t *testing.T) {
	cases := []struct {
		name    string
		results []protocol.SettingResult
		want    string
	}{
		{"all applied", []protocol.SettingResult{{Setting: "usb_storage_mode", Outcome: "applied"}}, stateSynced},
		{"unsupported", []protocol.SettingResult{
			{Setting: "usb_storage_mode", Outcome: "applied"},
			{Setting: "disable_task_mgr", Outcome: "unsupported", Reason: "Windows only"}}, statePartial},
		{"one failed", []protocol.SettingResult{
			{Setting: "usb_storage_mode", Outcome: "applied"},
			{Setting: "firewall_enabled", Outcome: "failed", Reason: "access denied"}}, statePartial},
		{"all failed", []protocol.SettingResult{
			{Setting: "usb_storage_mode", Outcome: "failed", Reason: "module in use"}}, stateFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, version := statusEnv(t)
			// "synced" is what an agent's summary would say; the results win.
			if code, body := e.report(map[string]any{"policy_version": version, "status": "synced",
				"results": c.results}); code != http.StatusOK {
				t.Fatalf("report -> %d %s", code, body)
			}
			s := e.state()
			if s.Status != c.want || len(s.Results) != len(c.results) {
				t.Errorf("state = %s with %d results, want %s with %d", s.Status, len(s.Results), c.want, len(c.results))
			}
		})
	}
}

func reconnectWith(t *testing.T, status string, results []protocol.SettingResult) int {
	t.Helper()
	e, version := statusEnv(t)
	if err := e.repo.UpdateDeviceState(context.Background(), &DeviceSystemPolicyState{
		DeviceID: "dev-1", PolicyVersion: version, Status: status, Results: results,
	}); err != nil {
		t.Fatal(err)
	}
	hub := &hookHub{}
	if err := NewHandler(e.repo, hub, nil, nopAudit{}, nil).SyncOnReconnect(context.Background(), "dev-1"); err != nil {
		t.Fatal(err)
	}
	return hub.sent
}

// A device that applied everything it can is not sent the same policy on every
// reconnect just because some settings do not exist on its OS.
func TestReconnectSkipsUnsupportedOnlyPartial(t *testing.T) {
	sent := reconnectWith(t, statePartial, []protocol.SettingResult{
		{Setting: "usb_storage_mode", Outcome: "applied"},
		{Setting: "disable_task_mgr", Outcome: "unsupported"},
	})
	if sent != 0 {
		t.Errorf("re-sent %d times to a device that cannot do better", sent)
	}
}

// A failed setting can succeed on a retry, so it is re-sent.
func TestReconnectRepushesFailedPartial(t *testing.T) {
	sent := reconnectWith(t, statePartial, []protocol.SettingResult{
		{Setting: "usb_storage_mode", Outcome: "applied"},
		{Setting: "firewall_enabled", Outcome: "failed"},
	})
	if sent != 1 {
		t.Errorf("sent %d times to a device with a failed setting, want 1", sent)
	}
}

package systempolicy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

// TestSummarizeLegacyStatus: a server without per-setting results gets what
// this agent always sent -- failed when something failed, synced otherwise
// (unsupported included, as before).
func TestSummarizeLegacyStatus(t *testing.T) {
	status, msg := Summarize([]protocol.SettingResult{
		{Setting: "usb_storage_mode", Outcome: protocol.OutcomeApplied},
		{Setting: "disable_task_mgr", Outcome: protocol.OutcomeUnsupported},
	})
	if status != "synced" || msg != "" {
		t.Errorf("no failures -> %q %q, want synced", status, msg)
	}
	status, msg = Summarize([]protocol.SettingResult{
		{Setting: "firewall_enabled", Outcome: protocol.OutcomeFailed, Reason: "access denied"},
	})
	if status != "failed" || msg != "firewall_enabled: access denied" {
		t.Errorf("a failure -> %q %q", status, msg)
	}
}

// TestReportSendsResultsAndRetries: the report carries the per-setting results,
// and a transient server error is retried instead of dropping the report (the
// caller used to discard it, leaving the device pending).
func TestReportSendsResultsAndRetries(t *testing.T) {
	var calls atomic.Int32
	var got struct {
		Results []protocol.SettingResult `json:"results"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	results := []protocol.SettingResult{{Setting: "usb_storage_mode", Outcome: protocol.OutcomeUnsupported, Reason: "macOS"}}
	if err := NewEngine(ts.URL, "dev-1", "s").Report(context.Background(), "v1", "synced", PolicySettings{}, "", results); err != nil {
		t.Fatalf("report after one 503: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("%d attempts, want a retry after the 503", calls.Load())
	}
	if len(got.Results) != 1 || got.Results[0].Outcome != protocol.OutcomeUnsupported {
		t.Errorf("server received results %+v", got.Results)
	}
}

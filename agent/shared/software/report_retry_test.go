package software

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A terminal report closes the task on the server. One dropped POST used to
// leave the task 'installing' until the sweep gave up on it, so a transient
// failure is retried; a progress step is superseded by the next one.
func TestReportProgressRetriesOnlyTerminalReports(t *testing.T) {
	for status, wantCalls := range map[string]int32{"success": 2, "failed": 2, "downloading": 1} {
		t.Run(status, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			_ = ReportProgress(context.Background(), srv.URL, "dev-1", "s", ProgressReport{TaskID: "t1", Status: status})
			if got := calls.Load(); got != wantCalls {
				t.Fatalf("%s: %d attempts, want %d", status, got, wantCalls)
			}
		})
	}
}

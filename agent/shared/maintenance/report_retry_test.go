package maintenance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	srv "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/maintenance"
)

// TestATransientReportFailureIsRetried is the regression test for a step lost
// to a single dropped POST: the agent used to log the error and move on, the
// row stayed 'dispatched', and the job stayed 'running' for the life of the
// installation. A 5xx on the first attempt must not be the last attempt.
func TestATransientReportFailureIsRetried(t *testing.T) {
	var calls int32
	var decoded srv.StepReport
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// Simulate a server that is restarting.
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			t.Errorf("decode report: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	e := engineFor(&captureServer{Server: ts}, "dev-42", "s3cret")
	if err := e.report(t.Context(), StepReport{
		TaskID: "t-9", Step: TaskCleanupTemp, Status: srv.TaskStatusFailed,
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server saw %d calls, want 2 (one failure, one retry)", got)
	}
	if decoded.TaskID != "t-9" {
		t.Errorf("task_id = %q, want t-9", decoded.TaskID)
	}
}

// TestAPermittedReportIsNotRetried bounds the other half: a 4xx means the same
// payload will be refused again, so retrying only burns time the sweep has
// left. The server's own vocabulary check is what produces these.
func TestAPermittedReportIsNotRetried(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(ts.Close)

	e := engineFor(&captureServer{Server: ts}, "dev-42", "s3cret")
	if err := e.report(t.Context(), StepReport{
		TaskID: "t-9", Step: TaskCleanupTemp, Status: "nonsense",
	}); err == nil {
		t.Fatal("report: want an error for a 4xx rejection")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server saw %d calls, want 1 (a 4xx is permanent)", got)
	}
}

// contextIsCancelled is a guard so the retry loop cannot outlive its step.
// A report posted after the step's context expired would carry a request the
// client refuses to send, and the retry would spin against a dead deadline.
func TestReportRetryStopsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be reached with a cancelled context")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)

	e := engineFor(&captureServer{Server: ts}, "dev-42", "s3cret")
	if err := e.report(ctx, StepReport{
		TaskID: "t-9", Step: TaskCleanupTemp, Status: srv.TaskStatusFailed,
	}); err == nil {
		t.Fatal("report: want an error when the context is already cancelled")
	}
}

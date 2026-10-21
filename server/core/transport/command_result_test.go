package transport

import (
	"context"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

type fakeReceiver struct {
	own               string
	gotStatus, gotErr string
	calls             int
}

func (f *fakeReceiver) HandleCommandResult(_ context.Context, _ string, id, status, errMsg string) bool {
	f.calls++
	if id != f.own {
		return false
	}
	f.gotStatus, f.gotErr = status, errMsg
	return true
}

type fakeQueue struct{ flushed []string }

func (q *fakeQueue) FlushOnReconnect(_ context.Context, deviceID string) error {
	q.flushed = append(q.flushed, deviceID)
	return nil
}

func newTestHandler(t *testing.T) *WSHandler {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	h := NewWSHandler(NewHub(), devicemgmt.NewRepository(database), database, time.Minute)
	t.Cleanup(h.Close)
	return h
}

// An agent that refuses work answers {"status":"rejected","error":...}, which
// it sends as a failed command result under the envelope id. For a software
// task or a patch job that id is the module's own row, not an agent_commands
// row; the module has to see the refusal, or the row claims 'dispatched'
// forever.
func TestCommandResult_ClaimedByModuleReceiver(t *testing.T) {
	h := newTestHandler(t)
	other := &fakeReceiver{own: "someone-else"}
	mine := &fakeReceiver{own: "task-1"}
	h.WithCommandResultReceiver(other).WithCommandResultReceiver(mine)

	h.handleCommandResult(context.Background(), &Conn{DeviceID: "dev-1"}, Envelope{
		Type: TypeCommandResult, ID: "task-1", Status: "failed",
		Result: map[string]any{"status": "rejected", "error": "queue full"},
	})

	if mine.gotStatus != "failed" || mine.gotErr != "queue full" {
		t.Fatalf("receiver got status=%q err=%q, want failed / queue full", mine.gotStatus, mine.gotErr)
	}
	if other.calls != 1 {
		t.Fatalf("first receiver asked %d times, want 1", other.calls)
	}
}

// Queued work must go out after hello, when the device's capabilities and
// version are the ones it is running.
func TestHello_FlushesReconnectQueues(t *testing.T) {
	h := newTestHandler(t)
	q := &fakeQueue{}
	h.WithReconnectQueue(q)

	h.handleHello(context.Background(), &Conn{DeviceID: "dev-1"}, Envelope{
		Type: TypeHello, Payload: map[string]any{"agent_version": "1.2.3", "name": "Windows"},
	})

	if len(q.flushed) != 1 || q.flushed[0] != "dev-1" {
		t.Fatalf("flushed %v after hello, want [dev-1]", q.flushed)
	}
}

func TestResultError(t *testing.T) {
	if got := resultError(map[string]any{"error": "bad payload"}); got != "bad payload" {
		t.Fatalf("resultError = %q", got)
	}
	if got := resultError(map[string]any{"status": "dispatched"}); got != "" {
		t.Fatalf("resultError without error = %q, want empty", got)
	}
	if got := resultError(nil); got != "" {
		t.Fatalf("resultError(nil) = %q", got)
	}
}

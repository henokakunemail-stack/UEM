package softwaredeployment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// queueHub records what each device would have been sent. A device is online
// only if listed, and a busy hub refuses every send.
type queueHub struct {
	mu     sync.Mutex
	online map[string]bool
	busy   bool
	sent   []sentEnvelope
}

type sentEnvelope struct {
	device, id, command string
}

func (h *queueHub) Online(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.online[id]
}

func (h *queueHub) SendTo(id string, b []byte) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.busy || !h.online[id] {
		return false
	}
	var env struct {
		ID      string `json:"id"`
		Command string `json:"command"`
	}
	_ = json.Unmarshal(b, &env)
	h.sent = append(h.sent, sentEnvelope{device: id, id: env.ID, command: env.Command})
	return true
}

func (h *queueHub) setOnline(id string, on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.online[id] = on
}

func (h *queueHub) sends() []sentEnvelope {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]sentEnvelope(nil), h.sent...)
}

func (f *packageFixture) deployTo(t *testing.T, device string) (taskID string) {
	t.Helper()
	rec := f.do(t, http.MethodPost, "/api/software/deployments",
		`{"name":"rollout","package_id":"pkg-1","target_type":"device","target_id":"`+device+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create deployment = %d %s", rec.Code, rec.Body.String())
	}
	if err := f.db.Get(&taskID, `SELECT id FROM deployment_tasks WHERE device_id = ?`, device); err != nil {
		t.Fatal(err)
	}
	return taskID
}

func (f *packageFixture) task(t *testing.T, id string) (status, errMsg string) {
	t.Helper()
	var row struct {
		Status string  `db:"status"`
		Err    *string `db:"error_message"`
	}
	if err := f.db.Get(&row, `SELECT status, error_message FROM deployment_tasks WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if row.Err != nil {
		errMsg = *row.Err
	}
	return row.Status, errMsg
}

// A deployment aimed at a laptop that was asleep used to leave its task
// pending forever: nothing ever sent it, and the toast said "queued". The task
// now goes out after the device's next hello.
func TestAQueuedTaskIsSentAfterTheDeviceReconnects(t *testing.T) {
	hub := &queueHub{online: map[string]bool{}}
	f := newPackageFixture(t, hub)
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")

	taskID := f.deployTo(t, "dev-1")
	if len(hub.sends()) != 0 {
		t.Fatal("a command was sent to an offline device")
	}
	if status, _ := f.task(t, taskID); status != TaskStatusPending {
		t.Fatalf("task for an offline device = %q, want pending", status)
	}

	hub.setOnline("dev-1", true)
	if err := f.handler.FlushOnReconnect(context.Background(), "dev-1"); err != nil {
		t.Fatal(err)
	}
	sends := hub.sends()
	if len(sends) != 1 || sends[0].id != taskID || sends[0].command != "software.install" {
		t.Fatalf("sends after reconnect = %+v, want one software.install with the task id as envelope id", sends)
	}
	if status, _ := f.task(t, taskID); status != TaskStatusDispatched {
		t.Fatalf("task after reconnect = %q, want dispatched", status)
	}

	// A second flush -- a reconnect storm -- sends nothing more.
	_ = f.handler.FlushOnReconnect(context.Background(), "dev-1")
	if len(hub.sends()) != 1 {
		t.Fatalf("sends after a second flush = %d, want 1", len(hub.sends()))
	}
}

// An agent that refuses a command answers with a failed command result under
// the envelope id. With a random id that answer matched nothing and the task
// said 'dispatched' forever on a healthy endpoint.
func TestAnAgentRefusalFailsTheTask(t *testing.T) {
	hub := &queueHub{online: map[string]bool{"dev-1": true}}
	f := newPackageFixture(t, hub)
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")
	taskID := f.deployTo(t, "dev-1")

	if f.handler.HandleCommandResult(context.Background(), "dev-2", taskID, "failed", "queue full") {
		t.Fatal("a result from another device claimed this task")
	}
	if !f.handler.HandleCommandResult(context.Background(), "dev-1", taskID, "failed", "queue full") {
		t.Fatal("the refusal for a deployment task was not claimed")
	}
	status, msg := f.task(t, taskID)
	if status != TaskStatusFailed || !strings.Contains(msg, "queue full") {
		t.Fatalf("task after refusal = %q %q, want failed with the agent's reason", status, msg)
	}
	var dep string
	if err := f.db.Get(&dep, `SELECT status FROM software_deployments`); err != nil {
		t.Fatal(err)
	}
	if dep != "failed" {
		t.Fatalf("deployment after its only task was refused = %q, want failed", dep)
	}
}

// Progress never moves a finished task back, and the server's own send mark
// never lands on top of the agent's progress.
func TestAFinishedTaskIsNotReopened(t *testing.T) {
	hub := &queueHub{online: map[string]bool{"dev-1": true}}
	f := newPackageFixture(t, hub)
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")
	taskID := f.deployTo(t, "dev-1")
	repo := &Repository{db: f.db}
	ctx := context.Background()

	if err := repo.UpdateTaskProgress(ctx, "dev-1", TaskProgressReport{TaskID: taskID, Status: TaskStatusSuccess}); err != nil {
		t.Fatal(err)
	}
	err := repo.UpdateTaskProgress(ctx, "dev-1", TaskProgressReport{TaskID: taskID, Status: TaskStatusInstalling})
	if !errors.Is(err, ErrTaskFinished) {
		t.Fatalf("late progress report: err = %v, want ErrTaskFinished", err)
	}
	if status, _ := f.task(t, taskID); status != TaskStatusSuccess {
		t.Fatalf("task after a late progress report = %q, want success", status)
	}
	if sent, _ := repo.MarkTaskSent(ctx, taskID); sent {
		t.Fatal("MarkTaskSent moved a finished task")
	}
}

// A device that never reported capabilities no longer blocks the rollout; an
// online one with an old agent gets a task that says why it failed.
func TestAnUnknownCapabilityIsSettledWhenTheDeviceAnswers(t *testing.T) {
	hub := &queueHub{online: map[string]bool{"dev-1": true}}
	f := newPackageFixture(t, hub)
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")
	f.db.MustExec(`UPDATE devices SET capabilities = NULL`)

	rec := f.do(t, http.MethodPost, "/api/software/deployments",
		`{"name":"rollout","package_id":"pkg-1","target_type":"group","target_id":"grp-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("deployment to devices with unknown capabilities = %d %s, want 201", rec.Code, rec.Body.String())
	}
	var online, offline struct {
		Status string  `db:"status"`
		Err    *string `db:"error_message"`
	}
	_ = f.db.Get(&online, `SELECT status, error_message FROM deployment_tasks WHERE device_id = 'dev-1'`)
	_ = f.db.Get(&offline, `SELECT status, error_message FROM deployment_tasks WHERE device_id = 'dev-2'`)
	if online.Status != TaskStatusFailed || online.Err == nil || !strings.HasPrefix(*online.Err, "unsupported_agent") {
		t.Fatalf("online device without capabilities: %+v, want failed unsupported_agent", online)
	}
	if offline.Status != TaskStatusPending {
		t.Fatalf("offline device without capabilities = %q, want pending until it answers", offline.Status)
	}

	// dev-2 reconnects with a current agent: hello stores its capabilities,
	// then the flush sends.
	f.db.MustExec(`UPDATE devices SET capabilities = '["software.install"]' WHERE id = 'dev-2'`)
	hub.setOnline("dev-2", true)
	_ = f.handler.FlushOnReconnect(context.Background(), "dev-2")
	if len(hub.sends()) != 1 || hub.sends()[0].device != "dev-2" {
		t.Fatalf("sends = %+v, want one to dev-2", hub.sends())
	}
}

// A send that failed on a busy socket is retried while the device stays
// connected; it never reconnects, so the flush alone would never run.
func TestABusySendIsRetried(t *testing.T) {
	hub := &queueHub{online: map[string]bool{"dev-1": true}, busy: true}
	f := newPackageFixture(t, hub)
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")
	taskID := f.deployTo(t, "dev-1")
	if status, _ := f.task(t, taskID); status != TaskStatusPending {
		t.Fatalf("task after a busy send = %q, want pending", status)
	}

	hub.busy = false
	f.handler.ResendQueued(context.Background())
	if len(hub.sends()) != 1 {
		t.Fatalf("sends after the retry = %d, want 1", len(hub.sends()))
	}
}

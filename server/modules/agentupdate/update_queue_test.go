package agentupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// recordingHub stands in for the transport hub. online decides what Online()
// reports; every SendTo is captured so a test can assert what the agent would
// actually have been told to do.
//
// SendTo mirrors the real hub: transport.Hub.SendTo looks the connection up and
// returns false when there is none, so an offline device must fail the send
// rather than accept it. A stub that accepted unconditionally would let a test
// pass on behaviour production never has.
type recordingHub struct {
	mu      sync.Mutex
	online  bool
	sent    [][]byte
	sendErr bool // the connection exists but is momentarily busy
}

func (h *recordingHub) Online(string) bool { return h.online }

func (h *recordingHub) SendTo(_ string, msg []byte) bool {
	if h.sendErr || !h.online {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, append([]byte(nil), msg...))
	return true
}

func (h *recordingHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sent)
}

func (h *recordingHub) command(t *testing.T, i int) string {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	var msg struct {
		Command string `json:"command"`
		ID      string `json:"id"`
		Payload struct {
			TargetVersion string `json:"target_version"`
			DownloadURL   string `json:"download_url"`
			SHA256        string `json:"sha256_checksum"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(h.sent[i], &msg); err != nil {
		t.Fatalf("command %d is not JSON: %v (%s)", i, err, h.sent[i])
	}
	if msg.Command != "update.apply" {
		t.Errorf("command = %q, want \"update.apply\"", msg.Command)
	}
	return msg.ID
}

type queueFixture struct {
	h    *Handler
	repo *Repository
	hub  *recordingHub
	db   *sqlx.DB
	r    *chi.Mux
}

// newQueueFixture builds the production handler over a real migrated database,
// with two enrolled devices: one connected, one not.
func newQueueFixture(t *testing.T, online bool) *queueFixture {
	t.Helper()

	database, err := db.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC()
	seedDevice := func(id, status string) {
		if _, err := database.Exec(`
			INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
				device_secret_hash, enrolled_at, created_at, updated_at)
			VALUES (?, ?, 'windows', '11', '1.0.0', ?, ?, ?, ?, ?)`,
			id, id, status, devicemgmt.HashToken("secret-"+id), now, now, now); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seedDevice("dev-offline", "offline")
	seedDevice("dev-online", "online")

	if _, err := database.Exec(`
		INSERT INTO agent_releases (id, version, os_name, arch, file_path, file_size,
			sha256_checksum, changelog, is_active, uploaded_by, created_at)
		VALUES ('rel-2', '2.0.0', 'windows', 'amd64', 'x', 42, 'abc123', '', 1, 'admin', ?)`,
		now); err != nil {
		t.Fatal(err)
	}

	hub := &recordingHub{online: online}
	repo := NewRepository(database)
	h := NewHandler(repo, hub, devicemgmt.NewRepository(database), discardAuditor{},
		t.TempDir(), func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The dispatch route requires technician; the real middleware
				// derives this from the session, which these tests do not build.
				next.ServeHTTP(w, r.WithContext(rbac.WithRole(r.Context(), rbac.RoleAdmin)))
			})
		})

	r := chi.NewRouter()
	h.Register(r)
	return &queueFixture{h: h, repo: repo, hub: hub, db: database, r: r}
}

func (f *queueFixture) dispatch(t *testing.T, deviceID, version string) *httptest.ResponseRecorder {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", deviceID)
	req := httptest.NewRequest(http.MethodPost,
		"/api/devices/"+deviceID+"/update/dispatch",
		strings.NewReader(`{"target_version":"`+version+`"}`))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	return rec
}

func (f *queueFixture) taskStatus(t *testing.T, deviceID string) (status, errMsg string) {
	t.Helper()
	if err := f.db.Get(&status,
		`SELECT status FROM device_update_tasks WHERE device_id = ?`, deviceID); err != nil {
		t.Fatal(err)
	}
	_ = f.db.Get(&errMsg,
		`SELECT error_message FROM device_update_tasks WHERE device_id = ?`, deviceID)
	return status, errMsg
}

// TestDispatchToAnOfflineDeviceLeavesAFindablePendingTask is the regression test
// for a response that promised something the code did not do.
//
// handleDispatchDeviceUpdate answered {"status":"queued","message":"device is
// currently offline; update queued"} -- but it created the row with status
// 'dispatched' and stamped dispatched_at *before* it ever called hub.Online, so
// the queued path and the dispatched path left identical rows. Nothing re-read
// them: update.apply is only built at the two dispatch sites, both behind
// hub.Online, and the offline sweeper in software-deployment/sweep.go sweeps
// deployment_tasks, not device_update_tasks. There is no read of this table
// anywhere in server/core or server/cmd.
//
// So the operator was told the update was queued, the row claimed it had been
// sent, and the upgrade silently never happened. Nothing points at why, and
// dispatched_at is a timestamp for a dispatch that did not occur.
func TestDispatchToAnOfflineDeviceLeavesAFindablePendingTask(t *testing.T) {
	f := newQueueFixture(t, false)

	rec := f.dispatch(t, "dev-offline", "2.0.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch -> %d %s, want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"queued"`) {
		t.Fatalf("body = %s, want the queued status the response claims", rec.Body.String())
	}
	if f.hub.count() != 0 {
		t.Errorf("%d commands sent to an offline device", f.hub.count())
	}

	status, _ := f.taskStatus(t, "dev-offline")
	if status != TaskStatusPending {
		t.Errorf("task status = %q, want %q: the row claims it was sent to a device "+
			"that was never connected, and nothing would ever look at it again",
			status, TaskStatusPending)
	}

	// The pending set is what the reconnect path scans. If it cannot find this
	// row, the fix is cosmetic and the promise is still a lie.
	pending, err := f.repo.PendingTasksFor(context.Background(), "dev-offline")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("PendingTasksFor returned %d tasks, want 1: a queued update the "+
			"reconnect path cannot see is not queued, it is lost", len(pending))
	}
	if pending[0].TargetVersion != "2.0.0" {
		t.Errorf("pending task target = %q, want \"2.0.0\"", pending[0].TargetVersion)
	}
}

// TestTheQueuedUpdateIsDeliveredWhenTheDeviceReconnects is the other half: the
// flush has to run and put a real command on the socket.
func TestTheQueuedUpdateIsDeliveredWhenTheDeviceReconnects(t *testing.T) {
	f := newQueueFixture(t, false)
	if rec := f.dispatch(t, "dev-offline", "2.0.0"); rec.Code != http.StatusOK {
		t.Fatalf("queue -> %d %s", rec.Code, rec.Body.String())
	}

	// Still offline: the flush must not claim the task was sent.
	if err := f.h.FlushPendingUpdates(context.Background(), "dev-offline"); err != nil {
		t.Fatalf("flush to an offline device: %v", err)
	}
	if f.hub.count() != 0 {
		t.Fatalf("%d commands sent to an offline device", f.hub.count())
	}
	if status, _ := f.taskStatus(t, "dev-offline"); status != TaskStatusPending {
		t.Errorf("status = %q after a flush that could not send, want %q: a task "+
			"marked sent when the send failed is dropped on the floor", status, TaskStatusPending)
	}

	// The device connects and the transport calls the hook.
	f.hub.online = true
	if err := f.h.FlushPendingUpdates(context.Background(), "dev-offline"); err != nil {
		t.Fatalf("flush on reconnect: %v", err)
	}

	if f.hub.count() != 1 {
		t.Fatalf("%d commands sent on reconnect, want 1", f.hub.count())
	}
	f.hub.command(t, 0)
	if status, _ := f.taskStatus(t, "dev-offline"); status != "dispatched" {
		t.Errorf("status = %q after a successful send, want \"dispatched\"", status)
	}

	// A second reconnect must not re-send. update.apply swaps a binary; sending
	// it twice is not idempotent.
	if err := f.h.FlushPendingUpdates(context.Background(), "dev-offline"); err != nil {
		t.Fatal(err)
	}
	if f.hub.count() != 1 {
		t.Errorf("%d commands after a second flush, want 1: the update was sent twice", f.hub.count())
	}
}

// TestTheSendIsStampedBeforeItIsWritten closes the hole the other ordering
// leaves. hub.SendTo fails when the connection is momentarily busy, which is a
// real outcome on a flapping link; if the stamp went after the write, the task
// would read 'dispatched' having sent nothing and be skipped forever after.
func TestTheSendIsStampedBeforeItIsWritten(t *testing.T) {
	f := newQueueFixture(t, false)
	if rec := f.dispatch(t, "dev-offline", "2.0.0"); rec.Code != http.StatusOK {
		t.Fatalf("queue -> %d", rec.Code)
	}

	f.hub.online = true
	f.hub.sendErr = true
	if err := f.h.FlushPendingUpdates(context.Background(), "dev-offline"); err != nil {
		t.Fatalf("flush with a busy connection: %v", err)
	}
	if status, _ := f.taskStatus(t, "dev-offline"); status != TaskStatusPending {
		t.Errorf("status = %q after a send that failed, want %q: it must stay "+
			"findable for the next reconnect", status, TaskStatusPending)
	}

	// The connection frees up; the retry gets through.
	f.hub.sendErr = false
	if err := f.h.FlushPendingUpdates(context.Background(), "dev-offline"); err != nil {
		t.Fatal(err)
	}
	if f.hub.count() != 1 {
		t.Errorf("%d commands on retry, want 1", f.hub.count())
	}
	if status, _ := f.taskStatus(t, "dev-offline"); status != "dispatched" {
		t.Errorf("status = %q after a successful retry, want \"dispatched\"", status)
	}
}

// TestTwoReconnectsSendTheUpdateOnce covers the race the conditional stamp
// exists for: both goroutines read the same pending set before either writes.
func TestTwoReconnectsSendTheUpdateOnce(t *testing.T) {
	f := newQueueFixture(t, false)
	if rec := f.dispatch(t, "dev-offline", "2.0.0"); rec.Code != http.StatusOK {
		t.Fatalf("queue -> %d", rec.Code)
	}
	f.hub.online = true

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_ = f.h.FlushPendingUpdates(context.Background(), "dev-offline")
		}()
	}
	wg.Wait()

	if f.hub.count() != 1 {
		t.Errorf("%d update commands for one queued task across two concurrent "+
			"reconnects, want 1", f.hub.count())
	}
}

// TestAPendingTaskWhoseReleaseIsGoneFailsWithAReason: the release can be
// deactivated or deleted between queueing and reconnect. Failing the task is
// right. Failing it silently is not -- the operator is left watching a task
// that never resolves with nothing to act on.
func TestAPendingTaskWhoseReleaseIsGoneFailsWithAReason(t *testing.T) {
	f := newQueueFixture(t, false)
	if rec := f.dispatch(t, "dev-offline", "2.0.0"); rec.Code != http.StatusOK {
		t.Fatalf("queue -> %d", rec.Code)
	}
	if _, err := f.db.Exec(`UPDATE agent_releases SET is_active = 0`); err != nil {
		t.Fatal(err)
	}
	f.hub.online = true

	if err := f.h.FlushPendingUpdates(context.Background(), "dev-offline"); err != nil {
		t.Fatalf("flush: %v", err)
	}

	status, errMsg := f.taskStatus(t, "dev-offline")
	if status != "failed" {
		t.Errorf("status = %q, want \"failed\"", status)
	}
	if errMsg == "" {
		t.Error("error_message is empty; a failed task with no reason gives the operator nothing to act on")
	}
	if f.hub.count() != 0 {
		t.Errorf("%d commands sent with no active release", f.hub.count())
	}
}

// TestDispatchToAnOnlineDeviceStillSendsImmediately keeps the ordinary path
// working. If this fails, no update ever reaches a connected device.
func TestDispatchToAnOnlineDeviceStillSendsImmediately(t *testing.T) {
	f := newQueueFixture(t, true)

	rec := f.dispatch(t, "dev-online", "2.0.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch -> %d %s", rec.Code, rec.Body.String())
	}
	if f.hub.count() != 1 {
		t.Fatalf("%d commands to an online device, want 1", f.hub.count())
	}
	f.hub.command(t, 0)
	if status, _ := f.taskStatus(t, "dev-online"); status != "dispatched" {
		t.Errorf("status = %q, want \"dispatched\"", status)
	}

	// Nothing left pending, so a reconnect has nothing to replay.
	pending, err := f.repo.PendingTasksFor(context.Background(), "dev-online")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("%d tasks still pending after an immediate dispatch, want 0", len(pending))
	}
}

// TestFlushingADeviceWithNothingQueuedIsANoOp: the hook runs on every single
// agent connection, so the common case must not touch the database more than it
// has to and must not report an error.
func TestFlushingADeviceWithNothingQueuedIsANoOp(t *testing.T) {
	f := newQueueFixture(t, true)
	if err := f.h.FlushPendingUpdates(context.Background(), "dev-online"); err != nil {
		t.Fatalf("flush with an empty queue: %v", err)
	}
	if f.hub.count() != 0 {
		t.Errorf("%d commands sent with an empty queue", f.hub.count())
	}
}

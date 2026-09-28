package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// These tests cover the HTTP surface only. The repository's own behaviour is
// covered in repository_test.go; what is asserted here is the contract the
// console and the agent actually depend on — status codes, JSON shapes, and the
// trust boundary on the agent endpoint. Those were previously unasserted
// anywhere, so a later edit could drop the device-ownership guard and leave the
// suite green.

type stubHub struct {
	online   map[string]bool
	sendFail map[string]bool
	sent     []string
}

func newStubHub() *stubHub {
	return &stubHub{online: map[string]bool{}, sendFail: map[string]bool{}}
}

func (h *stubHub) Online(id string) bool { return h.online[id] }
func (h *stubHub) SendTo(id string, _ []byte) bool {
	if h.sendFail[id] {
		return false
	}
	h.sent = append(h.sent, id)
	return true
}

type stubAudit struct{ calls int }

func (s *stubAudit) Log(context.Context, string, string, string, string, map[string]string) error {
	s.calls++
	return nil
}

// stubSecrets resolves one known secret to one known device. HashToken is the
// same hash the real AuthenticateAgent applies, so the test exercises the real
// comparison rather than a bypass.
type stubSecrets struct {
	byHash map[string]devicemgmt.Device
}

func (s *stubSecrets) FindBySecretHash(_ context.Context, hash string) (devicemgmt.Device, error) {
	if d, ok := s.byHash[hash]; ok {
		return d, nil
	}
	return devicemgmt.Device{}, context.Canceled
}

const (
	testSecretA = "secret-alpha"
	testSecretB = "secret-beta"
)

func newHandlerTest(t *testing.T) (*sqlx.DB, *Handler, *stubHub, *stubSecrets) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "handler.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	hub := newStubHub()
	secrets := &stubSecrets{byHash: map[string]devicemgmt.Device{
		devicemgmt.HashToken(testSecretA): {ID: "dev-a", Hostname: "PC-A", Status: "online"},
		devicemgmt.HashToken(testSecretB): {ID: "dev-b", Hostname: "PC-B", Status: "online"},
	}}

	h := NewHandler(NewRepository(d), hub, &stubAudit{}, passthroughAuth, secrets)
	return d, h, hub, secrets
}

func passthroughAuth(next http.Handler) http.Handler { return next }

// roleContext injects the user id and role the real auth middleware would set,
// so rbac.RequireRole sees what it sees in production.
func roleContext(r *http.Request, role string) *http.Request {
	ctx := context.WithValue(r.Context(), auth.CtxUserID, "user-1")
	ctx = rbac.WithRole(ctx, role)
	return r.WithContext(ctx)
}

// call runs one request through the full registered router so chi routing, the
// auth middleware, and rbac are all exercised as they are in production.
func call(t *testing.T, h *Handler, method, path, role string, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	h.Register(r)

	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, rdr)
	if role != "" {
		req = roleContext(req, role)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func seedOnlineDevice(t *testing.T, d *sqlx.DB, id, hostname string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := d.Exec(`
		INSERT INTO devices (id, hostname, os_name, status, enrolled_at,
		                     device_secret_hash, created_at, updated_at)
		VALUES (?, ?, 'windows', 'online', ?, 'secret_hash', ?, ?)`,
		id, hostname, now, now, now)
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

func TestCreateJobRequiresTechnician(t *testing.T) {
	_, h, _, _ := newHandlerTest(t)

	// A viewer must not be able to start a fleet sweep.
	w := call(t, h, http.MethodPost, "/api/maintenance/jobs", rbac.RoleViewer, RunRequest{
		TaskType: TaskCleanupTemp, TargetType: TargetAll,
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("viewer POST = %d, want 403 (a viewer must not start a sweep)", w.Code)
	}
}

func TestCreateJobRejectsUnknownTaskTypeAndCreatesNoRow(t *testing.T) {
	d, h, _, _ := newHandlerTest(t)
	seedOnlineDevice(t, d, "dev-a", "PC-A")

	w := call(t, h, http.MethodPost, "/api/maintenance/jobs", rbac.RoleTechnician, RunRequest{
		TaskType: "frobnicate", TargetType: TargetAll,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown task_type = %d, want 400", w.Code)
	}
	var count int
	if err := d.Get(&count, `SELECT COUNT(*) FROM maintenance_jobs`); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if count != 0 {
		t.Fatalf("job row created for an unknown task_type (count=%d)", count)
	}
}

func TestCreateJobRequiresTargetIDForDevice(t *testing.T) {
	_, h, _, _ := newHandlerTest(t)
	w := call(t, h, http.MethodPost, "/api/maintenance/jobs", rbac.RoleTechnician, RunRequest{
		TaskType: TaskCleanupTemp, TargetType: TargetDevice, TargetID: "  ",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("blank target_id = %d, want 400", w.Code)
	}
}

func TestCreateJobResponseEnvelope(t *testing.T) {
	d, h, hub, _ := newHandlerTest(t)
	seedOnlineDevice(t, d, "dev-a", "PC-A")
	hub.online["dev-a"] = true

	w := call(t, h, http.MethodPost, "/api/maintenance/jobs", rbac.RoleTechnician, RunRequest{
		TaskType: TaskCleanupTemp, TargetType: TargetAll,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201: %s", w.Code, w.Body.String())
	}
	var got struct {
		Job struct {
			TotalTasks int `json:"total_tasks"`
			Dispatched int `json:"dispatched"`
			Skipped    int `json:"skipped"`
		} `json:"job"`
		TotalTargets   int `json:"total_targets"`
		DispatchedLive int `json:"dispatched_live"`
		Skipped        int `json:"skipped"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// MaintenancePage reads exactly these four keys.
	if got.TotalTargets == 0 {
		t.Error("total_targets is 0; the console gates its poll stop on this")
	}
	// Values, not just keys. The console divides by job.total_tasks to draw the
	// progress bar, so a re-read that silently shipped the zeroed in-memory
	// struct would pin the bar at 0% forever while the test still passed on
	// key presence alone.
	if got.Job.TotalTasks != got.TotalTargets {
		t.Errorf("job.total_tasks = %d, want %d (CreateJob inserted it as zero and only SQL wrote it)",
			got.Job.TotalTasks, got.TotalTargets)
	}
	if got.Job.Dispatched+got.Job.Skipped != got.TotalTargets {
		t.Errorf("job.dispatched+job.skipped = %d+%d = %d, want %d",
			got.Job.Dispatched, got.Job.Skipped,
			got.Job.Dispatched+got.Job.Skipped, got.TotalTargets)
	}
	if got.DispatchedLive != got.Job.Dispatched {
		t.Errorf("envelope dispatched_live = %d, job.dispatched = %d; the console reads both",
			got.DispatchedLive, got.Job.Dispatched)
	}
}

func TestListEndpointsReturnArraysNotNull(t *testing.T) {
	_, h, _, _ := newHandlerTest(t)
	for _, path := range []string{"/api/maintenance/tasks", "/api/maintenance/jobs"} {
		w := call(t, h, http.MethodGet, path, rbac.RoleViewer, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, w.Code)
		}
		var body any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET %s: decode: %v", path, err)
		}
		if body == nil {
			t.Errorf("GET %s returned null; the console calls .map() on it", path)
		}
	}
}

// seedTaskForReport creates a job with one task and returns the task id.
func seedTaskForReport(t *testing.T, d *sqlx.DB) string {
	t.Helper()
	ctx := context.Background()
	repo := NewRepository(d)
	job := &Job{Name: "j", TaskType: TaskFullScan, TargetType: string(TargetAll), CreatedBy: "u1"}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	targets, _ := repo.ResolveTargets(ctx, TargetAll, "")
	if err := repo.CreateTasksForJob(ctx, job.ID, TaskFullScan, targets); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	tasks, _ := repo.GetTasksForJob(ctx, job.ID)
	if len(tasks) != 1 {
		t.Fatalf("want 1 task, got %d", len(tasks))
	}
	return tasks[0].ID
}

func TestAgentResultRequiresDeviceCredentials(t *testing.T) {
	d, h, _, _ := newHandlerTest(t)
	seedOnlineDevice(t, d, "dev-a", "PC-A")
	taskID := seedTaskForReport(t, d)

	// No secret at all.
	req := httptest.NewRequest(http.MethodPost, "/api/agent/maintenance/tasks/"+taskID+"/result",
		bytes.NewReader([]byte(`{"step":"cleanup_temp","status":"running"}`)))
	w := httptest.NewRecorder()
	router := chi.NewRouter()
	h.Register(router)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated agent report = %d, want 401", w.Code)
	}
}

func TestAgentResultRejectsCrossDeviceReport(t *testing.T) {
	d, h, _, _ := newHandlerTest(t)
	seedOnlineDevice(t, d, "dev-a", "PC-A")
	seedOnlineDevice(t, d, "dev-b", "PC-B")
	repo := NewRepository(d)
	// Point the job at dev-a only, so dev-a owns the task.
	job := &Job{Name: "j", TaskType: TaskFullScan, TargetType: string(TargetDevice), TargetID: "dev-a", CreatedBy: "u1"}
	if err := repo.CreateJob(context.Background(), job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	targets, _ := repo.ResolveTargets(context.Background(), TargetDevice, "dev-a")
	if err := repo.CreateTasksForJob(context.Background(), job.ID, TaskFullScan, targets); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	tasks, _ := repo.GetTasksForJob(context.Background(), job.ID)
	taskID := tasks[0].ID

	// dev-b presents its OWN valid secret but reports on dev-a's task.
	body := bytes.NewReader([]byte(`{"step":"cleanup_temp","status":"running","bytes_freed":9999}`))
	req := httptest.NewRequest(http.MethodPost, "/api/agent/maintenance/tasks/"+taskID+"/result", body)
	req.Header.Set("X-Device-Id", "dev-b")
	req.Header.Set("X-Device-Secret", testSecretB)
	w := httptest.NewRecorder()
	router := chi.NewRouter()
	h.Register(router)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-device report = %d, want 403", w.Code)
	}
	// The task must be untouched.
	got, err := repo.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.BytesFreed != 0 || got.Status != TaskStatusPending {
		t.Fatalf("task mutated by a rejected cross-device report: status=%q bytes=%d",
			got.Status, got.BytesFreed)
	}
}

func TestAgentResultRejectsServerOnlySkippedStatus(t *testing.T) {
	d, h, hub, _ := newHandlerTest(t)
	seedOnlineDevice(t, d, "dev-a", "PC-A")
	hub.online["dev-a"] = true
	repo := NewRepository(d)
	taskID := seedTaskForReport(t, d)
	if err := repo.MarkTaskDispatched(context.Background(), taskID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	body := bytes.NewReader([]byte(`{"step":"cleanup_temp","status":"skipped"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/agent/maintenance/tasks/"+taskID+"/result", body)
	req.Header.Set("X-Device-Id", "dev-a")
	req.Header.Set("X-Device-Secret", testSecretA)
	w := httptest.NewRecorder()
	router := chi.NewRouter()
	h.Register(router)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("agent-posted skipped = %d, want 400", w.Code)
	}
	got, _ := repo.GetTask(context.Background(), taskID)
	if got.Status == TaskStatusSkipped {
		t.Fatal("task was marked skipped by an agent report")
	}
}

func TestAgentResultRejectsBodyTaskIDMismatch(t *testing.T) {
	d, h, _, _ := newHandlerTest(t)
	seedOnlineDevice(t, d, "dev-a", "PC-A")
	taskID := seedTaskForReport(t, d)

	body := bytes.NewReader([]byte(`{"task_id":"some-other-task","step":"cleanup_temp","status":"running"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/agent/maintenance/tasks/"+taskID+"/result", body)
	req.Header.Set("X-Device-Id", "dev-a")
	req.Header.Set("X-Device-Secret", testSecretA)
	w := httptest.NewRecorder()
	router := chi.NewRouter()
	h.Register(router)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("body/URL task_id mismatch = %d, want 400", w.Code)
	}
}

func TestProgressEndpointShape(t *testing.T) {
	d, h, _, _ := newHandlerTest(t)
	seedOnlineDevice(t, d, "dev-a", "PC-A")
	taskID := seedTaskForReport(t, d)
	_ = taskID

	ctx := context.Background()
	job, err := NewRepository(d).GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	w := call(t, h, http.MethodGet, "/api/maintenance/jobs/"+job.JobID+"/progress", rbac.RoleViewer, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("progress = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// MaintenancePage reads these off the progress object.
	for _, k := range []string{"bytes_freed", "reboot_required", "remaining", "status"} {
		if _, ok := body[k]; !ok {
			t.Errorf("progress object missing %q", k)
		}
	}
}

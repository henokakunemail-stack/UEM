package agentupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

func TestZeroTaskCampaignAutoCompletes(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "zero_tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	campaign := &UpdateCampaign{
		ID:            "camp-zero",
		Name:          "Zero Tasks",
		TargetVersion: "1.4.0",
		TargetType:    "all",
		Status:        "in_progress",
		CreatedBy:     "admin",
	}
	if err := repo.CreateCampaign(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}

	// Trigger sync on campaign with 0 tasks
	if err := repo.SyncCampaignStatusByID(context.Background(), campaign.ID); err != nil {
		t.Fatalf("sync campaign: %v", err)
	}

	c, err := repo.GetCampaign(context.Background(), campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "completed" {
		t.Errorf("got status %q, want 'completed' for zero-task campaign", c.Status)
	}
}

func TestAbandonOrphanedTasksReapsOfflineAndTimesOut(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "orphan_sweep.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	now := time.Now().UTC()
	oldTime := now.Add(-20 * time.Minute)

	repo := NewRepository(database)
	campaign := &UpdateCampaign{
		ID:            "camp-orphan",
		Name:          "Orphan Test",
		TargetVersion: "1.4.0",
		TargetType:    "all",
		Status:        "in_progress",
		CreatedBy:     "admin",
	}
	if err := repo.CreateCampaign(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}

	// Seed offline device
	_, err = database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
			device_secret_hash, enrolled_at, created_at, updated_at)
		VALUES ('dev-offline-1', 'OFFLINE-PC', 'windows', '11', '1.0.0', 'offline', 'dummyhash', ?, ?, ?)`,
		oldTime, oldTime, oldTime)
	if err != nil {
		t.Fatal(err)
	}

	task := &DeviceUpdateTask{
		CampaignID:    &campaign.ID,
		DeviceID:      "dev-offline-1",
		FromVersion:   "1.0.0",
		TargetVersion: "1.4.0",
		// Sent, and then the device went away without reporting. A task that
		// was only queued is not reaped here; see TestQueuedTasks*.
		Status:       "dispatched",
		DispatchedAt: &oldTime,
		CreatedAt:    oldTime,
	}
	if err := repo.CreateUpdateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	// Manually backdate created_at in database
	_, _ = database.Exec(`UPDATE device_update_tasks SET created_at = ?, dispatched_at = ? WHERE id = ?`,
		oldTime, oldTime, task.ID)

	reaped, err := repo.AbandonOrphanedTasks(context.Background(), 10*time.Minute)
	if err != nil {
		t.Fatalf("abandon orphaned: %v", err)
	}
	if reaped < 1 {
		t.Errorf("expected at least 1 reaped task, got %d", reaped)
	}

	// Campaign should have rolled up to failed (all tasks failed)
	c, err := repo.GetCampaign(context.Background(), campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "failed" {
		t.Errorf("got status %q, want 'failed'", c.Status)
	}
}

func TestCancelCampaignCancelsTasksAndStatus(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "cancel_campaign.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	now := time.Now().UTC()
	repo := NewRepository(database)
	campaign := &UpdateCampaign{
		ID:            "camp-cancel",
		Name:          "Cancel Test",
		TargetVersion: "1.4.0",
		TargetType:    "all",
		Status:        "in_progress",
		CreatedBy:     "admin",
	}
	if err := repo.CreateCampaign(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}

	_, _ = database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
			device_secret_hash, enrolled_at, created_at, updated_at)
		VALUES ('dev-cancel-1', 'HOST-CANCEL', 'windows', '11', '1.0.0', 'online', 'secret', ?, ?, ?)`,
		now, now, now)

	task := &DeviceUpdateTask{
		CampaignID:    &campaign.ID,
		DeviceID:      "dev-cancel-1",
		FromVersion:   "1.0.0",
		TargetVersion: "1.4.0",
		Status:        TaskStatusPending,
	}
	if err := repo.CreateUpdateTask(context.Background(), task); err != nil {
		t.Fatalf("create update task: %v", err)
	}

	if ok, err := repo.CancelCampaign(context.Background(), campaign.ID); err != nil || !ok {
		t.Fatalf("cancel campaign: ok=%v err=%v", ok, err)
	}

	c, err := repo.GetCampaign(context.Background(), campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "cancelled" {
		t.Errorf("got status %q, want 'cancelled'", c.Status)
	}

	tasks, err := repo.ListCampaignTasks(context.Background(), campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "failed" {
		t.Errorf("expected 1 task with status 'failed', got %+v", tasks)
	}
}

func TestListCampaignTasksAndLogsEndpoints(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "tasks_logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	now := time.Now().UTC()
	repo := NewRepository(database)
	campaign := &UpdateCampaign{
		ID:            "camp-inspect",
		Name:          "Inspect Test",
		TargetVersion: "1.4.0",
		TargetType:    "all",
		Status:        "in_progress",
		CreatedBy:     "admin",
	}
	if err := repo.CreateCampaign(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}

	_, _ = database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
			device_secret_hash, enrolled_at, created_at, updated_at)
		VALUES ('dev-inspect-1', 'HOST-ALPHA', 'windows', '11', '1.0.0', 'online', 'secret', ?, ?, ?)`,
		now, now, now)

	task := &DeviceUpdateTask{
		CampaignID:    &campaign.ID,
		DeviceID:      "dev-inspect-1",
		FromVersion:   "1.0.0",
		TargetVersion: "1.4.0",
		Status:        TaskStatusPending,
	}
	_ = repo.CreateUpdateTask(context.Background(), task)

	// Insert audit log row
	_, _ = database.Exec(`
		INSERT INTO audit_logs (id, actor_type, actor_id, action, target_type, target_id, details, created_at)
		VALUES ('log-1', 'system', 'dev-inspect-1', 'agent_update.task_dispatched', 'dev-inspect-1', 'dev-inspect-1', '{"campaign_id":"camp-inspect"}', ?)`,
		now)

	h := NewHandler(repo, nil, devicemgmt.NewRepository(database), discardAuditor{},
		t.TempDir(), func(next http.Handler) http.Handler { return next }, "")
	r := chi.NewRouter()
	h.Register(r)

	// GET /api/agent-updates/campaigns/{id}/tasks with admin role context
	req := httptest.NewRequest(http.MethodGet, "/api/agent-updates/campaigns/camp-inspect/tasks", nil)
	req = req.WithContext(rbac.WithRole(req.Context(), rbac.RoleAdmin))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get tasks status %d: %s", rec.Code, rec.Body.String())
	}

	// GET /api/agent-updates/campaigns/{id}/logs with admin role context
	req2 := httptest.NewRequest(http.MethodGet, "/api/agent-updates/campaigns/camp-inspect/logs", nil)
	req2 = req2.WithContext(rbac.WithRole(req2.Context(), rbac.RoleAdmin))
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("get logs status %d: %s", rec2.Code, rec2.Body.String())
	}
}

package agentupdate

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
	"github.com/jmoiron/sqlx"
)

// campaignFixture is a rollout over two devices, already started, with one
// update task per device.
type campaignFixture struct {
	http.Handler
	db       *sqlx.DB
	campaign string
	tasks    map[string]string // device id -> task id
}

func newCampaignFixture(t *testing.T, devices []string) *campaignFixture {
	t.Helper()

	database, err := db.Open(filepath.Join(t.TempDir(), "campaign.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC()
	repo := NewRepository(database)
	campaign := &UpdateCampaign{
		ID: "camp-1", Name: "Rollout", TargetVersion: "2.0.0",
		TargetType: "all", Status: "in_progress", CreatedBy: "admin",
	}
	if err := repo.CreateCampaign(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(repo, nil, devicemgmt.NewRepository(database), discardAuditor{},
		t.TempDir(), func(next http.Handler) http.Handler { return next }, "")

	tasks := map[string]string{}
	for _, id := range devices {
		secret := "secret-" + id
		if _, err := database.Exec(`
			INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
				device_secret_hash, enrolled_at, created_at, updated_at)
			VALUES (?, ?, 'windows', '11', '1.0.0', 'online', ?, ?, ?, ?)`,
			id, id, devicemgmt.HashToken(secret), now, now, now); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		task := &DeviceUpdateTask{
			DeviceID: id, FromVersion: "1.0.0", TargetVersion: "2.0.0", Status: TaskStatusPending,
		}
		task.CampaignID = &campaign.ID
		if err := repo.CreateUpdateTask(context.Background(), task); err != nil {
			t.Fatalf("create task for %s: %v", id, err)
		}
		tasks[id] = task.ID
	}

	r := chi.NewRouter()
	h.Register(r)
	return &campaignFixture{Handler: r, db: database, campaign: campaign.ID, tasks: tasks}
}

func (f *campaignFixture) status(t *testing.T) string {
	t.Helper()
	var status string
	if err := f.db.Get(&status, `SELECT status FROM update_campaigns WHERE id = ?`, f.campaign); err != nil {
		t.Fatal(err)
	}
	return status
}

func (f *campaignFixture) reportAs(t *testing.T, deviceID, status string) {
	t.Helper()
	body := `{"task_id":"` + f.tasks[deviceID] + `","status":"` + status +
		`","target_version":"2.0.0"}`
	rec := report(t, f.Handler, deviceID, "secret-"+deviceID, deviceID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s reporting %q -> %d %s", deviceID, status, rec.Code, rec.Body.String())
	}
}

// TestACampaignReachesCompletedWhenEveryDeviceReports is the regression test for a
// status that was written once and never moved again.
//
// handleStartCampaign set 'in_progress' and that was the only write to the
// column. The agent update path wrote device_update_tasks.status and returned,
// so a rollout that upgraded every endpoint it targeted stayed 'in_progress'
// for the life of the installation. The console's "Active Campaigns" KPI counts
// that exact status, so it kept reporting outstanding work that had finished,
// and the Start button remained offered on a campaign with nothing left to
// dispatch.
func TestACampaignReachesCompletedWhenEveryDeviceReports(t *testing.T) {
	f := newCampaignFixture(t, []string{"d1", "d2"})

	if got := f.status(t); got != "in_progress" {
		t.Fatalf("fixture status = %q, want \"in_progress\"", got)
	}

	f.reportAs(t, "d1", "success")
	if got := f.status(t); got != "in_progress" {
		t.Errorf("campaign status = %q after 1 of 2 devices, want \"in_progress\": "+
			"completing a rollout while an endpoint is still updating is the same "+
			"lie the run rollup had", got)
	}

	f.reportAs(t, "d2", "success")
	if got := f.status(t); got != "completed" {
		t.Errorf("campaign status = %q after every device succeeded, want "+
			"\"completed\"", got)
	}
}

// TestACampaignWhoseDevicesAllFailedIsFailed: a rollout that upgraded nothing
// must not read as a success.
func TestACampaignWhoseDevicesAllFailedIsFailed(t *testing.T) {
	f := newCampaignFixture(t, []string{"d1", "d2"})

	f.reportAs(t, "d1", "failed")
	f.reportAs(t, "d2", "rollback")

	if got := f.status(t); got != "failed" {
		t.Errorf("campaign status = %q after every device failed, want \"failed\"", got)
	}
}

// TestACampaignWithOneSuccessIsCompleted: the rollup asks whether everything
// finished, not whether everything succeeded.
func TestACampaignWithOneSuccessIsCompleted(t *testing.T) {
	f := newCampaignFixture(t, []string{"d1", "d2"})

	f.reportAs(t, "d1", "success")
	f.reportAs(t, "d2", "failed")

	if got := f.status(t); got != "completed" {
		t.Errorf("campaign status = %q, want \"completed\": the rollout ran and "+
			"every endpoint has reported", got)
	}
}

// TestACampaignIsNotRolledUpEarlyByAProgressReport: 'downloading' is not
// terminal, so a rollout mid-flight must not be summarised as finished.
func TestACampaignIsNotRolledUpEarlyByAProgressReport(t *testing.T) {
	f := newCampaignFixture(t, []string{"d1"})

	f.reportAs(t, "d1", "downloading")

	if got := f.status(t); got != "in_progress" {
		t.Errorf("campaign status = %q with the device still downloading, want "+
			"\"in_progress\"", got)
	}
}

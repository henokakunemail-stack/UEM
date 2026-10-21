package agentupdate

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// seedDevice adds one more enrolled device to a queue fixture.
func (f *queueFixture) seedDevice(t *testing.T, id, osName, status string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := f.db.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
			device_secret_hash, enrolled_at, created_at, updated_at)
		VALUES (?, ?, ?, '11', '1.0.0', ?, ?, ?, ?, ?)`,
		id, id, osName, status, devicemgmt.HashToken("secret-"+id), now, now, now); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func (f *queueFixture) campaignWithBatch(t *testing.T, batch int) string {
	t.Helper()
	c := &UpdateCampaign{
		Name: "Waves", TargetVersion: "2.0.0", TargetType: "all",
		BatchSize: batch, StaggerIntervalSec: 60, Status: "draft", CreatedBy: "admin",
	}
	if err := f.repo.CreateCampaign(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func (f *queueFixture) countTasks(t *testing.T, campaignID, where string) int {
	t.Helper()
	var n int
	if err := f.db.Get(&n, `SELECT COUNT(*) FROM device_update_tasks WHERE campaign_id = ? AND `+where, campaignID); err != nil {
		t.Fatal(err)
	}
	return n
}

// Waves used to be a goroutine per campaign that created tasks as it went. A
// first wave that produced no task -- its devices already current, or with no
// release -- made the rollup see zero tasks and write 'completed' while later
// waves were still to come; a restart lost every wave not yet sent. Every task
// now exists from the start and the next wave is a timestamp in the database.
func TestCampaignWavesSurviveAndNothingClosesEarly(t *testing.T) {
	f := newQueueFixture(t, true)
	f.hub.onlineSet = map[string]bool{"dev-online": true, "dev-3": true}
	f.seedDevice(t, "dev-3", "windows", "online")
	f.seedDevice(t, "dev-linux", "linux", "online") // no linux release exists
	id := f.campaignWithBatch(t, 1)

	rec := f.post(t, "/api/agent-updates/campaigns/"+id+"/start")
	if rec.Code != http.StatusOK {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}

	// dev-offline, dev-online, dev-3 need the update; dev-linux has no release.
	if got := f.taskCount(t, id); got != 4 {
		t.Fatalf("tasks after start = %d, want 4: every target gets its task up front", got)
	}
	var reason string
	if err := f.db.Get(&reason, `SELECT error_message FROM device_update_tasks WHERE device_id = 'dev-linux'`); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reason, "no_release") {
		t.Fatalf("device without a release: error = %q, want a no_release failure instead of a silent drop", reason)
	}
	if got := f.countTasks(t, id, "released_at IS NULL"); got != 2 {
		t.Fatalf("tasks waiting for a wave = %d, want 2 (batch size 1)", got)
	}
	if s := f.campaignStatus(t, id); s != "in_progress" {
		t.Fatalf("status after the first wave = %q, want in_progress while waves remain", s)
	}

	// Nothing about the waves lives in memory: the schedule is a timestamp a
	// restarted server or a new leader reads back. Each wave is made due by
	// moving that timestamp, never by inventing it.
	advanceWave := func() {
		t.Helper()
		var next *time.Time
		if err := f.db.Get(&next, `SELECT next_wave_at FROM update_campaigns WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if next == nil {
			t.Fatal("next_wave_at is NULL while tasks wait for a wave: no node would ever release them")
		}
		f.db.MustExec(`UPDATE update_campaigns SET next_wave_at = ? WHERE id = ?`, next.Add(-time.Hour), id)
		f.h.WaveTick(context.Background())
	}
	advanceWave()
	advanceWave()

	if got := f.countTasks(t, id, "released_at IS NULL"); got != 0 {
		t.Fatalf("tasks still waiting after two due waves = %d, want 0", got)
	}
	var next *time.Time
	if err := f.db.Get(&next, `SELECT next_wave_at FROM update_campaigns WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Fatalf("next_wave_at = %v after the last wave, want NULL", next)
	}
	if s := f.campaignStatus(t, id); s != "in_progress" {
		t.Fatalf("status = %q with devices still updating, want in_progress", s)
	}
}

// An update queued for a device that is away is what the reconnect flush is
// for. The sweep used to fail it ten minutes later, so the flush had nothing
// to send; it now waits a week.
func TestQueuedTasksWaitForTheDeviceUpToSevenDays(t *testing.T) {
	f := newQueueFixture(t, false)
	if rec := f.dispatch(t, "dev-offline", "2.0.0"); rec.Code != http.StatusOK {
		t.Fatalf("dispatch = %d %s", rec.Code, rec.Body.String())
	}
	old := time.Now().UTC().Add(-20 * time.Minute)
	f.db.MustExec(`UPDATE device_update_tasks SET created_at = ?, released_at = ?, dispatched_at = ? WHERE device_id = 'dev-offline'`, old, old, old)

	if _, err := f.repo.AbandonOrphanedTasks(context.Background(), TaskTimeoutGrace); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.taskStatus(t, "dev-offline"); status != TaskStatusPending {
		t.Fatalf("queued task after 20 minutes offline = %q, want it still pending", status)
	}

	gone := time.Now().UTC().Add(-QueuedTaskExpiry - time.Hour)
	f.db.MustExec(`UPDATE device_update_tasks SET released_at = ? WHERE device_id = 'dev-offline'`, gone)
	if _, err := f.repo.AbandonOrphanedTasks(context.Background(), TaskTimeoutGrace); err != nil {
		t.Fatal(err)
	}
	status, msg := f.taskStatus(t, "dev-offline")
	if status != "failed" || !strings.HasPrefix(msg, "never_connected") {
		t.Fatalf("queued task after 7 days = %q %q, want failed never_connected", status, msg)
	}
}

// A slow download that keeps reporting is alive. The stale reap measured from
// the send, so it killed every update that took longer than 15 minutes.
func TestATaskThatKeepsReportingIsNotReaped(t *testing.T) {
	f := newQueueFixture(t, true)
	if rec := f.dispatch(t, "dev-online", "2.0.0"); rec.Code != http.StatusOK {
		t.Fatalf("dispatch = %d %s", rec.Code, rec.Body.String())
	}
	var taskID string
	if err := f.db.Get(&taskID, `SELECT id FROM device_update_tasks WHERE device_id = 'dev-online'`); err != nil {
		t.Fatal(err)
	}
	sent := time.Now().UTC().Add(-40 * time.Minute)
	f.db.MustExec(`UPDATE device_update_tasks SET dispatched_at = ?, created_at = ? WHERE id = ?`, sent, sent, taskID)
	if err := f.repo.RecordTaskProgress(context.Background(), taskID, "downloading", ""); err != nil {
		t.Fatal(err)
	}

	if _, err := f.repo.AbandonOrphanedTasks(context.Background(), TaskTimeoutGrace); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.taskStatus(t, "dev-online"); status != "downloading" {
		t.Fatalf("task reporting a minute ago = %q, want it left downloading", status)
	}

	f.db.MustExec(`UPDATE device_update_tasks SET progress_at = ? WHERE id = ?`, sent, taskID)
	if _, err := f.repo.AbandonOrphanedTasks(context.Background(), TaskTimeoutGrace); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.taskStatus(t, "dev-online"); status != "failed" {
		t.Fatalf("task silent for 40 minutes = %q, want failed", status)
	}
}

// A send that failed on a busy socket leaves the task queued. A device that
// stays connected never reconnects, so the wave loop retries it.
func TestTheWaveLoopRetriesAQueuedTaskForAnOnlineDevice(t *testing.T) {
	f := newQueueFixture(t, true)
	f.hub.sendErr = true
	if rec := f.dispatch(t, "dev-online", "2.0.0"); rec.Code == http.StatusOK {
		t.Fatalf("dispatch with a busy socket = 200, want a failure the fixture relies on")
	}
	if status, _ := f.taskStatus(t, "dev-online"); status != TaskStatusPending {
		t.Fatalf("task after a failed send = %q, want pending", status)
	}

	f.hub.sendErr = false
	f.h.WaveTick(context.Background())
	if f.hub.count() != 1 {
		t.Fatalf("commands sent by the retry = %d, want 1", f.hub.count())
	}
	if status, _ := f.taskStatus(t, "dev-online"); status != "dispatched" {
		t.Fatalf("task after the retry = %q, want dispatched", status)
	}
}

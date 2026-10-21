package agentupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func (f *queueFixture) newCampaign(t *testing.T, status string) string {
	t.Helper()
	c := &UpdateCampaign{
		Name: "Rollout", TargetVersion: "2.0.0", TargetType: "all",
		BatchSize: 50, Status: status, CreatedBy: "admin",
	}
	if err := f.repo.CreateCampaign(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func (f *queueFixture) post(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	return rec
}

func (f *queueFixture) taskCount(t *testing.T, campaignID string) int {
	t.Helper()
	var n int
	if err := f.db.Get(&n, `SELECT COUNT(*) FROM device_update_tasks WHERE campaign_id = ?`, campaignID); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *queueFixture) campaignStatus(t *testing.T, id string) string {
	t.Helper()
	var s string
	if err := f.db.Get(&s, `SELECT status FROM update_campaigns WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	return s
}

// A double click, or a second POST, used to run the whole rollout again and
// give every device a second task and a second update.apply.
func TestStartingACampaignTwiceIsRefused(t *testing.T) {
	f := newQueueFixture(t, true)
	id := f.newCampaign(t, "draft")

	if rec := f.post(t, "/api/agent-updates/campaigns/"+id+"/start"); rec.Code != http.StatusOK {
		t.Fatalf("first start = %d %s", rec.Code, rec.Body.String())
	}
	tasks, sends := f.taskCount(t, id), f.hub.count()
	if tasks == 0 {
		t.Fatal("the first start created no tasks; the fixture is not exercising the rollout")
	}

	rec := f.post(t, "/api/agent-updates/campaigns/"+id+"/start")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"campaign_not_startable"`) {
		t.Fatalf("second start = %d %s, want 409 campaign_not_startable", rec.Code, rec.Body.String())
	}
	if f.taskCount(t, id) != tasks || f.hub.count() != sends {
		t.Fatalf("the refused start still dispatched: tasks %d -> %d, sends %d -> %d",
			tasks, f.taskCount(t, id), sends, f.hub.count())
	}
}

// A cancelled campaign stays cancelled, on both start branches: the one that
// dispatches and the one that finds every device already up to date.
func TestStartingACancelledCampaignIsRefused(t *testing.T) {
	for name, upToDate := range map[string]bool{"dispatch branch": false, "all up to date": true} {
		t.Run(name, func(t *testing.T) {
			f := newQueueFixture(t, true)
			if upToDate {
				f.db.MustExec(`UPDATE devices SET agent_version = '2.0.0'`)
			}
			id := f.newCampaign(t, "cancelled")
			rec := f.post(t, "/api/agent-updates/campaigns/"+id+"/start")
			if rec.Code != http.StatusConflict {
				t.Fatalf("start of a cancelled campaign = %d %s, want 409", rec.Code, rec.Body.String())
			}
			if s := f.campaignStatus(t, id); s != "cancelled" {
				t.Fatalf("status = %q, want it to stay cancelled", s)
			}
			if f.taskCount(t, id) != 0 {
				t.Fatal("a cancelled campaign created tasks")
			}
		})
	}
}

// Cancelling fails the open tasks; a later rollup -- a late agent report, the
// sweeper -- then saw every task done and wrote 'completed' over 'cancelled'.
func TestRollupDoesNotOverwriteACancelledCampaign(t *testing.T) {
	f := newQueueFixture(t, true)
	id := f.newCampaign(t, "draft")
	if rec := f.post(t, "/api/agent-updates/campaigns/"+id+"/start"); rec.Code != http.StatusOK {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.post(t, "/api/agent-updates/campaigns/"+id+"/cancel"); rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	// A device that was already installing reports success afterwards.
	f.db.MustExec(`UPDATE device_update_tasks SET status = 'success' WHERE campaign_id = ?`, id)
	if err := f.repo.SyncCampaignStatusByID(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if s := f.campaignStatus(t, id); s != "cancelled" {
		t.Fatalf("status = %q after the rollup, want cancelled", s)
	}
}

// Cancelling a finished campaign is refused, and it no longer fails tasks of
// a campaign it did not cancel.
func TestCancellingAFinishedCampaignIsRefusedAndTouchesNoTask(t *testing.T) {
	f := newQueueFixture(t, true)
	id := f.newCampaign(t, "completed")
	task := &DeviceUpdateTask{DeviceID: "dev-online", FromVersion: "1.0.0", TargetVersion: "2.0.0", Status: TaskStatusPending}
	task.CampaignID = &id
	if err := f.repo.CreateUpdateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	rec := f.post(t, "/api/agent-updates/campaigns/"+id+"/cancel")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"campaign_not_cancellable"`) {
		t.Fatalf("cancel of a completed campaign = %d %s, want 409 campaign_not_cancellable", rec.Code, rec.Body.String())
	}
	var status string
	_ = f.db.Get(&status, `SELECT status FROM device_update_tasks WHERE id = ?`, task.ID)
	if status != TaskStatusPending {
		t.Fatalf("task status = %q, want it untouched", status)
	}
	if ok, err := f.repo.CancelCampaign(context.Background(), id); err != nil || ok {
		t.Fatalf("repository cancel of a completed campaign: ok=%v err=%v, want false", ok, err)
	}
	_ = f.db.Get(&status, `SELECT status FROM device_update_tasks WHERE id = ?`, task.ID)
	if status != TaskStatusPending {
		t.Fatalf("task status = %q after a cancel that matched nothing, want it untouched", status)
	}
}

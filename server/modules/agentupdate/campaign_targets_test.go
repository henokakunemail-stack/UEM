package agentupdate

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestCampaignRecordsDevicesAlreadyOnTarget: a campaign whose targets all ran
// the target version closed as "completed" with no task rows, so the console
// showed "0 devices" and could not say which devices had been checked. Every
// device the campaign looks at now has a row; current ones are up_to_date.
func TestCampaignRecordsDevicesAlreadyOnTarget(t *testing.T) {
	f := newQueueFixture(t, true)
	f.db.MustExec(`UPDATE devices SET agent_version = '2.0.0'`)
	id := f.campaignWithBatch(t, 10)

	rec := f.post(t, "/api/agent-updates/campaigns/"+id+"/start")
	if rec.Code != http.StatusOK {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	tasks, err := f.repo.ListCampaignTasks(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("%d task rows, want one per checked device (2)", len(tasks))
	}
	for _, task := range tasks {
		if task.Status != TaskStatusUpToDate || task.FromVersion != "2.0.0" {
			t.Errorf("%s: status %q from %q, want up_to_date from 2.0.0", task.DeviceID, task.Status, task.FromVersion)
		}
	}
	if s := f.campaignStatus(t, id); s != "completed" {
		t.Errorf("campaign status = %q, want completed: nothing was left to do", s)
	}
	list, err := f.repo.ListCampaigns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.ID == id && (c.TotalDevices != 2 || c.UpToDateDevices != 2 || c.FailedDevices != 0) {
			t.Errorf("campaign counts = total %d, up_to_date %d, failed %d; want 2, 2, 0",
				c.TotalDevices, c.UpToDateDevices, c.FailedDevices)
		}
	}
	if len(f.hub.sent) != 0 {
		t.Errorf("%d updates sent to devices already on the target version", len(f.hub.sent))
	}
}

// TestRetiredDevicesAreNotCampaignTargets: retirement is recorded in
// retired_at, and the target query filtered on status != 'retired', which never
// matched, so retired machines got update tasks that could never finish.
func TestRetiredDevicesAreNotCampaignTargets(t *testing.T) {
	f := newQueueFixture(t, true)
	f.seedDevice(t, "dev-retired", "windows", "offline")
	f.db.MustExec(`UPDATE devices SET retired_at = ? WHERE id = 'dev-retired'`, time.Now().UTC())
	f.db.MustExec(`INSERT INTO device_groups (id, name, created_at, updated_at) VALUES ('g1', 'Lab', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	f.db.MustExec(`INSERT INTO device_group_members (group_id, device_id, added_at) VALUES ('g1', 'dev-retired', CURRENT_TIMESTAMP), ('g1', 'dev-online', CURRENT_TIMESTAMP)`)

	for _, target := range []struct{ typ, id string }{{"all", ""}, {"group", "g1"}, {"device", "dev-retired"}} {
		ids, err := f.repo.ResolveTargetDevices(context.Background(), target.typ, target.id)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if id == "dev-retired" {
				t.Errorf("target %s/%s includes the retired device", target.typ, target.id)
			}
		}
	}

	id := f.campaignWithBatch(t, 10)
	if rec := f.post(t, "/api/agent-updates/campaigns/"+id+"/start"); rec.Code != http.StatusOK {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	if n := f.countTasks(t, id, "device_id = 'dev-retired'"); n != 0 {
		t.Errorf("the retired device got %d update task(s)", n)
	}
}

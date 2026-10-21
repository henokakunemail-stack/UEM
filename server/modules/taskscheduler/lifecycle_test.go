package taskscheduler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func (f *runFixture) fullHandler() *Handler {
	return &Handler{repo: f.repo, scheduler: NewScheduler(f.repo, stubHub{}), audit: nopAuditor{}, devices: f.secrets}
}

func (f *runFixture) createSchedule(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.fullHandler().createSchedule(rec, httptest.NewRequest(http.MethodPost, "/api/schedules", strings.NewReader(body)))
	return rec
}

// A schedule that cannot run used to save fine: a non-numeric or zero
// interval was skipped silently on every poll, and a missing target produced
// runs that reached nothing.
func TestScheduleValidationRefusesWhatCannotRun(t *testing.T) {
	f := newRunFixture(t, []string{"d1"})
	base := func(target, expr string) string {
		return `{"name":"n","script_id":"script-1","schedule_type":"interval",` + target + `,"schedule_expr":"` + expr + `"}`
	}
	cases := []struct{ name, body, code string }{
		{"zero interval", base(`"target_type":"all"`, "0"), "invalid_interval"},
		{"text interval", base(`"target_type":"all"`, "hourly"), "invalid_interval"},
		{"interval over 30 days", base(`"target_type":"all"`, "43201"), "invalid_interval"},
		{"unknown target type", base(`"target_type":"fleet"`, "60"), "invalid_target"},
		{"group without id", base(`"target_type":"group"`, "60"), "invalid_target"},
		{"empty group", base(`"target_type":"group","target_id":"grp-none"`, "60"), "no_targets"},
		{"retired device", base(`"target_type":"device","target_id":"ghost"`, "60"), "no_targets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.createSchedule(t, tc.body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("create = %d %s, want 400 %s", rec.Code, rec.Body.String(), tc.code)
			}
		})
	}
	if rec := f.createSchedule(t, base(`"target_type":"all"`, "60")); rec.Code != http.StatusCreated {
		t.Fatalf("a valid schedule = %d %s, want 201", rec.Code, rec.Body.String())
	}
}

// A run whose target resolved to nothing reached no device. It used to be
// written as a green "completed".
func TestARunWithNoTargetsFailsAndSaysWhy(t *testing.T) {
	f := newRunFixture(t, []string{"d1"})
	f.db.MustExec(`INSERT INTO task_schedules (id, name, script_id, target_type, target_id,
		schedule_type, schedule_expr, is_enabled, created_by, created_at, updated_at)
		VALUES ('sched-ghost','g','script-1','device','ghost','interval','60',1,'u1',?,?)`, time.Now().UTC(), time.Now().UTC())

	run, err := NewScheduler(f.repo, stubHub{}).TriggerSchedule(context.Background(), "sched-ghost", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || run.ErrorMessage == nil || !strings.HasPrefix(*run.ErrorMessage, "no_targets") {
		t.Fatalf("run = %q %v, want failed no_targets", run.Status, run.ErrorMessage)
	}
}

// The previous run of a schedule is still going: firing again ran the same
// script twice at once on every target.
func TestARunningScheduleIsNotOverlapped(t *testing.T) {
	f := newRunFixture(t, []string{"d1"}) // the fixture's run is still running
	h := f.fullHandler()

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "sched-1")
	req := httptest.NewRequest(http.MethodPost, "/api/schedules/sched-1/trigger", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.triggerSchedule(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "run_in_progress") {
		t.Fatalf("manual trigger = %d %s, want 409 run_in_progress", rec.Code, rec.Body.String())
	}

	f.db.MustExec(`UPDATE task_schedules SET last_run_at = ? WHERE id = 'sched-1'`, time.Now().UTC().Add(-2*time.Hour))
	h.scheduler.pollDueSchedules(context.Background())
	var runs int
	if err := f.db.Get(&runs, `SELECT COUNT(*) FROM scheduled_task_runs WHERE schedule_id = 'sched-1'`); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("runs after a due poll = %d, want 1: the poller overlapped a running run", runs)
	}
	// Not claimed either: claiming moved last_run_at, so the schedule lost its
	// turn and waited a whole interval after the slow run finished.
	var last time.Time
	if err := f.db.Get(&last, `SELECT last_run_at FROM task_schedules WHERE id = 'sched-1'`); err != nil {
		t.Fatal(err)
	}
	if time.Since(last) < time.Hour {
		t.Fatalf("last_run_at moved to %v by a poll that did not run anything", last)
	}

	_, err := h.scheduler.TriggerSchedule(context.Background(), "sched-1", "u1")
	if !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("TriggerSchedule while running: err = %v, want ErrRunInProgress", err)
	}
}

// A result that arrives after the sweep failed the device run used to
// overwrite it and re-roll the parent.
func TestALateReportDoesNotRewriteAFinishedDeviceRun(t *testing.T) {
	f := newRunFixture(t, []string{"d1"})
	dr := f.deviceRunFor(t, "d1")
	if err := f.repo.UpdateDeviceRunResult(context.Background(), dr, "failed", -1, "", "reaped"); err != nil {
		t.Fatal(err)
	}
	if rec := f.report(t, "d1", dr, "success"); rec.Code != http.StatusOK {
		t.Fatalf("late report -> %d", rec.Code)
	}
	var status string
	if err := f.db.Get(&status, `SELECT status FROM scheduled_task_device_runs WHERE id = ?`, dr); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("device run after a late report = %q, want it left failed", status)
	}
}

// A lost command or result on a device that stays online left the run
// 'running' forever: the orphan sweep only looks at offline devices.
func TestAStaleRunOnAnOnlineDeviceIsReaped(t *testing.T) {
	f := newRunFixture(t, []string{"d1", "d2"})
	old := time.Now().UTC().Add(-(300*time.Second + StaleGrace + time.Minute))
	f.db.MustExec(`UPDATE scheduled_task_device_runs SET started_at = ? WHERE id = ?`, old, f.deviceRunFor(t, "d1"))

	n, err := f.repo.AbandonStaleRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reaped %d device runs, want 1 (d2 is within its timeout)", n)
	}
	var status string
	_ = f.db.Get(&status, `SELECT status FROM scheduled_task_device_runs WHERE id = ?`, f.deviceRunFor(t, "d1"))
	if status != "failed" {
		t.Fatalf("stale device run = %q, want failed", status)
	}
}

// A retired member of a group is not a target; it used to be, and failed as
// "device is offline" on every run.
func TestARetiredGroupMemberIsNotATarget(t *testing.T) {
	f := newRunFixture(t, []string{"d1", "d2"})
	now := time.Now().UTC()
	f.db.MustExec(`INSERT INTO device_groups (id, name, created_at, updated_at) VALUES ('g1','G',?,?)`, now, now)
	f.db.MustExec(`INSERT INTO device_group_members (group_id, device_id, added_at) VALUES ('g1','d1',?), ('g1','d2',?)`, now, now)
	f.db.MustExec(`UPDATE devices SET retired_at = ? WHERE id = 'd2'`, now)

	ids, err := f.repo.GetTargetDeviceIDs(context.Background(), "group", "g1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "d1" {
		t.Fatalf("group targets = %v, want [d1]", ids)
	}
}

// The agent reports a cancelled script as "cancelled". The rollup never
// counted that as done, so the run never closed.
func TestACancelledScriptClosesTheRun(t *testing.T) {
	f := newRunFixture(t, []string{"d1"})
	if rec := f.report(t, "d1", f.deviceRunFor(t, "d1"), "cancelled"); rec.Code != http.StatusOK {
		t.Fatalf("report -> %d", rec.Code)
	}
	if status, _ := f.parent(t); status != "failed" {
		t.Fatalf("parent after a cancelled script = %q, want failed", status)
	}
}

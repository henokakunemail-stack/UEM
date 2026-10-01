package integration

import (
	"context"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/modules/taskscheduler"
)

func TestTaskScheduler_Lifecycle(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := taskscheduler.NewRepository(database)
	scheduler := taskscheduler.NewScheduler(repo, nil) // hub is nil for unit/integration repo test
	ctx := context.Background()
	now := time.Now().UTC()

	// 1. Script Repository: Create Script
	script := &taskscheduler.ScriptTemplate{
		Name:           "Purge Temporary Cache",
		Description:    "Deletes user and system temp folders safely",
		ScriptType:     "powershell",
		ScriptContent:  "Remove-Item -Path $env:TEMP\\* -Recurse -Force -ErrorAction SilentlyContinue",
		DefaultArgs:    "",
		TimeoutSeconds: 120,
		CreatedBy:      "admin-user-id",
	}
	if err := repo.CreateScript(ctx, script); err != nil {
		t.Fatalf("create script: %v", err)
	}
	if script.SHA256Hash == "" {
		t.Fatal("expected SHA-256 hash to be generated")
	}
	t.Logf("Script created: %s (SHA256: %s...)", script.Name, script.SHA256Hash[:12])

	// Verify List. Migration 0018 seeds starter scripts, so the table is not
	// empty on a migrated database; what matters is that the script just
	// created is in the list.
	scripts, err := repo.ListScripts(ctx)
	if err != nil {
		t.Fatalf("list scripts: %v", err)
	}
	found := false
	for _, s := range scripts {
		if s.ID == script.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created script %s missing from list of %d", script.ID, len(scripts))
	}

	// 2. Seed Mock Devices
	_, err = database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at)
		VALUES
			('dev-sched-1', 'WS-CLIENT-01', 'windows', '11.0', '1.0.0', 'Jakarta', 'online', ?, ?, 'h1', ?, ?),
			('dev-sched-2', 'WS-CLIENT-02', 'windows', '10.0', '1.0.0', 'Surabaya', 'online', ?, ?, 'h2', ?, ?)
	`, now, now, now, now, now, now, now, now)
	if err != nil {
		t.Fatal("seed devices:", err)
	}

	// 3. Create Schedule
	sched := &taskscheduler.TaskSchedule{
		Name:         "Nightly Temp Cleanup",
		Description:  "Runs every night on all enterprise endpoints",
		ScriptID:     script.ID,
		TargetType:   "all",
		TargetID:     "",
		ScheduleType: "cron",
		ScheduleExpr: "0 2 * * *",
		IsEnabled:    true,
		CreatedBy:    "admin-user-id",
	}
	if err := repo.CreateSchedule(ctx, sched); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	t.Logf("Schedule created: %s (Target: %s, Cron: %s)", sched.Name, sched.TargetType, sched.ScheduleExpr)

	// 4. Trigger Schedule Run
	run, err := scheduler.TriggerSchedule(ctx, sched.ID, "admin-user-id")
	if err != nil {
		t.Fatalf("trigger schedule: %v", err)
	}
	if run == nil || run.ID == "" {
		t.Fatal("expected valid run object")
	}
	t.Logf("Schedule triggered: Run ID %s (Status: %s)", run.ID, run.Status)

	// 5. Verify Target Device Runs
	devRuns, err := repo.ListDeviceRuns(ctx, run.ID)
	if err != nil {
		t.Fatalf("list device runs: %v", err)
	}
	if len(devRuns) != 2 {
		t.Fatalf("expected 2 device runs, got %d", len(devRuns))
	}
	t.Logf("Device runs verified: 2 targets dispatched")

	// 6. Report Result for Device 1
	firstDevRun := devRuns[0]
	outLog := "Deleted 320 MB temporary files successfully."
	if err := repo.UpdateDeviceRunResult(ctx, firstDevRun.ID, "success", 0, outLog, ""); err != nil {
		t.Fatalf("update device run result: %v", err)
	}

	// Verify update
	devRunsAfter, _ := repo.ListDeviceRuns(ctx, run.ID)
	var updatedDevRun taskscheduler.ScheduledTaskDeviceRun
	for _, dr := range devRunsAfter {
		if dr.ID == firstDevRun.ID {
			updatedDevRun = dr
			break
		}
	}
	if updatedDevRun.Status != "success" || *updatedDevRun.ExitCode != 0 || *updatedDevRun.OutputLog != outLog {
		t.Fatalf("unexpected updated dev run: %+v", updatedDevRun)
	}
	t.Logf("Device task result verified: Status=%s, ExitCode=%d, Output=%s",
		updatedDevRun.Status, *updatedDevRun.ExitCode, *updatedDevRun.OutputLog)

	// 7. Verify Schedule Run History
	runs, err := repo.ListRuns(ctx, sched.ID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("expected 1 run in history, got %d", len(runs))
	}
	t.Logf("Schedule execution history verified: %s (Run %s)", runs[0].ScheduleName, runs[0].ID)
}

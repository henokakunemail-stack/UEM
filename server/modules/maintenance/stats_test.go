package maintenance

import (
	"context"
	"testing"
	"time"
)

// The KPI cards were counted from the 50 jobs the list returns.
func TestJobStatsCountEveryJob(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	tx := d.MustBegin()
	for i := 0; i < 70; i++ {
		status := "completed"
		if i%10 == 0 {
			status = "running"
		}
		tx.MustExec(`INSERT INTO maintenance_jobs (id, name, task_type, target_type, target_id, created_by,
			total_tasks, dispatched, skipped, completed, failed, status, started_at)
			VALUES (?, 'j', 'disk_cleanup', 'all', '', 'u', 2, 2, 0, 1, 1, ?, ?)`, newID(), status, now)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s, err := NewRepository(d).JobStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 70 || s.Running != 7 || s.FailedTasks != 70 {
		t.Fatalf("stats = %+v, want 70 jobs, 7 running, 70 failed tasks", s)
	}
}

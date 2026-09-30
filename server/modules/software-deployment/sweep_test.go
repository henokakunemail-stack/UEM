package softwaredeployment

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, no cgo
)

// sweepFixture builds the three tables the reaper reads, with one deployment
// and one task per case.
func sweepFixture(t *testing.T) *sqlx.DB {
	t.Helper()
	db, err := sqlx.Open("sqlite", filepath.Join(t.TempDir(), "sweep.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	mustExec(t, db, `
		CREATE TABLE devices (
			id TEXT PRIMARY KEY, status TEXT NOT NULL DEFAULT 'online'
		);
		CREATE TABLE software_deployments (
			id TEXT PRIMARY KEY, status TEXT NOT NULL DEFAULT 'running', completed_at DATETIME
		);
		CREATE TABLE deployment_tasks (
			id TEXT PRIMARY KEY,
			deployment_id TEXT NOT NULL,
			device_id TEXT NOT NULL,
			status TEXT NOT NULL,
			error_message TEXT,
			updated_at DATETIME NOT NULL,
			completed_at DATETIME
		);
		INSERT INTO devices (id, status) VALUES ('dev-online', 'online'), ('dev-offline', 'offline');
		INSERT INTO software_deployments (id, status) VALUES ('dep1', 'running');`)

	return db
}

func addTask(t *testing.T, db *sqlx.DB, id, device, status string, age time.Duration) {
	t.Helper()
	mustExec(t, db, `INSERT INTO deployment_tasks
		(id, deployment_id, device_id, status, updated_at) VALUES (?, 'dep1', ?, ?, ?)`,
		id, device, status, time.Now().UTC().Add(-age))
}

// The reaper is the only thing standing between an agent that died mid-install
// and a rollout the console shows as running forever. Two properties matter and
// they pull in opposite directions: a task on a machine that is gone must be
// closed out, and a task on a machine that is merely slow must be left alone.
func TestAbandonOrphanedTasks(t *testing.T) {
	cases := []struct {
		name     string
		taskID   string
		device   string
		status   string
		age      time.Duration
		want     string
		wantReap bool
	}{
		{
			name:   "installing on an offline device is reaped",
			taskID: "t1", device: "dev-offline", status: "installing",
			age: AbandonGrace + time.Minute, want: TaskStatusFailedLost, wantReap: true,
		},
		{
			name:   "downloading on an offline device is reaped",
			taskID: "t2", device: "dev-offline", status: "downloading",
			age: AbandonGrace + time.Minute, want: TaskStatusFailedLost, wantReap: true,
		},
		{
			name:   "dispatched on an offline device is reaped",
			taskID: "t3", device: "dev-offline", status: "dispatched",
			age: AbandonGrace + time.Minute, want: TaskStatusFailedLost, wantReap: true,
		},
		{
			name:   "a live device's long install is never touched",
			taskID: "t4", device: "dev-online", status: "installing",
			age: AbandonGrace + time.Hour, want: "installing",
		},
		{
			name:   "an offline device that reported recently is given the grace window",
			taskID: "t5", device: "dev-offline", status: "installing",
			age: time.Minute, want: "installing",
		},
		{
			name:   "a task that already finished is not overwritten",
			taskID: "t6", device: "dev-offline", status: "success",
			age: AbandonGrace + time.Hour, want: "success",
		},
		{
			name:   "a real installer failure is not relabelled",
			taskID: "t7", device: "dev-offline", status: "failed",
			age: AbandonGrace + time.Hour, want: "failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := sweepFixture(t)
			addTask(t, db, tc.taskID, tc.device, tc.status, tc.age)

			repo := &Repository{db: db}
			n, err := repo.AbandonOrphanedTasks(context.Background(), AbandonGrace)
			if err != nil {
				t.Fatalf("AbandonOrphanedTasks: %v", err)
			}
			if tc.wantReap && n != 1 {
				t.Errorf("reaped %d tasks, want 1", n)
			}
			if !tc.wantReap && n != 0 {
				t.Errorf("reaped %d tasks, want 0", n)
			}

			var got string
			if err := db.Get(&got, `SELECT status FROM deployment_tasks WHERE id = ?`, tc.taskID); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

// A reaped task must carry the reason, or an operator staring at a red row has
// no way to tell a lost agent from a package that was rejected.
func TestAbandonOrphanedTasksExplainsItself(t *testing.T) {
	db := sweepFixture(t)
	addTask(t, db, "t1", "dev-offline", "installing", AbandonGrace+time.Minute)

	repo := &Repository{db: db}
	if _, err := repo.AbandonOrphanedTasks(context.Background(), AbandonGrace); err != nil {
		t.Fatalf("AbandonOrphanedTasks: %v", err)
	}

	var msg string
	if err := db.Get(&msg, `SELECT error_message FROM deployment_tasks WHERE id = 't1'`); err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Fatal("no error message; the operator cannot tell what happened")
	}
	var completed sqlNullTime
	if err := db.Get(&completed, `SELECT completed_at FROM deployment_tasks WHERE id = 't1'`); err != nil {
		t.Fatal(err)
	}
	if !completed.Valid {
		t.Fatal("completed_at is NULL; a terminal task with no completion time never sorts as done")
	}
}

// The reaper has to close the parent too, or the console keeps showing the
// rollout as running and the whole point of reaping is lost one level up.
func TestAbandonOrphanedTasksRollsUpDeployment(t *testing.T) {
	db := sweepFixture(t)
	addTask(t, db, "t1", "dev-offline", "installing", AbandonGrace+time.Minute)
	addTask(t, db, "t2", "dev-offline", "installing", AbandonGrace+time.Minute)
	addTask(t, db, "t3", "dev-offline", "installing", AbandonGrace+time.Minute)

	repo := &Repository{db: db}
	if _, err := repo.AbandonOrphanedTasks(context.Background(), AbandonGrace); err != nil {
		t.Fatalf("AbandonOrphanedTasks: %v", err)
	}

	var status string
	if err := db.Get(&status, `SELECT status FROM software_deployments WHERE id = 'dep1'`); err != nil {
		t.Fatal(err)
	}
	// Nothing succeeded, so 'failed' -- the same rule the 951c61f fix put in
	// place for real installer failures.
	if status != "failed" {
		t.Errorf("deployment status = %q, want %q", status, "failed")
	}
}

// A deployment that mixed a real success with a lost agent is completed, not
// failed: the operator needs to know the rollout partially worked.
func TestAbandonOrphanedTasksPartialRollup(t *testing.T) {
	db := sweepFixture(t)
	mustExec(t, db, `INSERT INTO deployment_tasks
		(id, deployment_id, device_id, status, updated_at, completed_at)
		VALUES ('t1', 'dep1', 'dev-offline', 'success', ?, ?)`, time.Now().UTC(), time.Now().UTC())
	addTask(t, db, "t2", "dev-offline", "installing", AbandonGrace+time.Minute)

	repo := &Repository{db: db}
	if _, err := repo.AbandonOrphanedTasks(context.Background(), AbandonGrace); err != nil {
		t.Fatalf("AbandonOrphanedTasks: %v", err)
	}

	var status string
	if err := db.Get(&status, `SELECT status FROM software_deployments WHERE id = 'dep1'`); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Errorf("deployment status = %q, want %q", status, "completed")
	}
}

// The sweep is idempotent: it runs every 15 seconds, so a second pass over the
// same row must be a no-op rather than rewriting completed_at forever.
func TestAbandonOrphanedTasksIsIdempotent(t *testing.T) {
	db := sweepFixture(t)
	addTask(t, db, "t1", "dev-offline", "installing", AbandonGrace+time.Minute)

	repo := &Repository{db: db}
	if _, err := repo.AbandonOrphanedTasks(context.Background(), AbandonGrace); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	var first sqlNullTime
	if err := db.Get(&first, `SELECT completed_at FROM deployment_tasks WHERE id = 't1'`); err != nil {
		t.Fatal(err)
	}

	n, err := repo.AbandonOrphanedTasks(context.Background(), AbandonGrace)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if n != 0 {
		t.Errorf("second pass reaped %d tasks, want 0", n)
	}
	var second sqlNullTime
	if err := db.Get(&second, `SELECT completed_at FROM deployment_tasks WHERE id = 't1'`); err != nil {
		t.Fatal(err)
	}
	if !first.Valid || !second.Valid {
		t.Fatal("completed_at is NULL")
	}
}

type sqlNullTime struct {
	Valid bool
	Time  time.Time
}

func (s *sqlNullTime) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		s.Valid = false
		return nil
	case time.Time:
		s.Valid, s.Time = true, v
		return nil
	}
	return nil
}

func (s *sqlNullTime) String() string { return s.Time.String() }

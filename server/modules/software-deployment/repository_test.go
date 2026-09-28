package softwaredeployment

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, no cgo
)

// The parent deployment rolls up from its tasks. A task that failed is still
// "done", but when nothing succeeded the deployment must read 'failed' rather
// than 'completed' -- the console renders that status as a green Completed
// badge, so getting it wrong hides a total failure behind a success.
func TestSyncDeploymentStatusRollsUpFailedWhenNothingSucceeded(t *testing.T) {
	cases := []struct {
		name  string
		tasks [][2]string // {status, exit_code}
		want  string
	}{
		{name: "all succeeded", tasks: [][2]string{{"success", "0"}, {"success", "0"}}, want: "completed"},
		{name: "partial failure keeps completed", tasks: [][2]string{{"success", "0"}, {"failed", "1603"}}, want: "completed"},
		{name: "all failed is failed", tasks: [][2]string{{"failed", "1603"}}, want: "failed"},
		{name: "still running stays running", tasks: [][2]string{{"success", "0"}, {"installing", ""}}, want: "running"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sqlx.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			mustExec(t, db, `
				CREATE TABLE software_deployments (
					id TEXT PRIMARY KEY, status TEXT NOT NULL DEFAULT 'running', completed_at DATETIME
				);
				CREATE TABLE deployment_tasks (
					id TEXT PRIMARY KEY, deployment_id TEXT NOT NULL,
					status TEXT NOT NULL, exit_code TEXT
				);
				INSERT INTO software_deployments (id, status) VALUES ('dep1', 'running');
			`)
			for i, task := range tc.tasks {
				mustExec(t, db, `INSERT INTO deployment_tasks (id, deployment_id, status, exit_code)
					VALUES (?, 'dep1', ?, ?)`,
					[]any{idStr(i), task[0], task[1]}...)
			}

			repo := &Repository{db: db}
			if err := repo.syncDeploymentStatus(context.Background(), idStr(0)); err != nil {
				t.Fatalf("syncDeploymentStatus: %v", err)
			}

			var got string
			if err := db.Get(&got, `SELECT status FROM software_deployments WHERE id = 'dep1'`); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func mustExec(t *testing.T, db *sqlx.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func idStr(i int) string {
	return string(rune('a' + i))
}

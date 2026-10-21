package securityaudit

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

func credDB(t *testing.T) *sqlx.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func addUser(t *testing.T, d *sqlx.DB, name, role, password string, created, updated time.Time) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	d.MustExec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"id-"+name, name, hash, role, created, updated)
}

// The check used to compare the hash with a truncated literal and passed every
// installation. Each way an installation keeps its setup credential must now
// be reported, and a changed strong password must pass.
func TestDefaultCredentialCheckFindsRealSetupPasswords(t *testing.T) {
	ctx := context.Background()
	created := time.Now().UTC().Add(-48 * time.Hour)
	later := created.Add(24 * time.Hour)

	cases := []struct {
		name     string
		setup    string
		password string
		updated  time.Time
		want     CheckStatus
		mention  string
	}{
		{"common password", "", "admin123", later, StatusFail, "common password"},
		{"still ADMIN_PASSWORD", "S3tup-Only!", "S3tup-Only!", later, StatusWarn, "ADMIN_PASSWORD"},
		{"generated, never changed", "", "9f3c1a7e5b2d8c4f6a0e1b3d", created, StatusWarn, "never been changed"},
		{"changed to a strong password", "S3tup-Only!", "correct horse battery staple 42", later, StatusPass, "checked"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := credDB(t)
			addUser(t, d, "admin", "admin", c.password, created, c.updated)
			addUser(t, d, "viewer1", "viewer", "admin", created, created) // not an admin: ignored
			e := NewEvaluator(&config.Config{}, d).WithSetupPassword(c.setup)
			st, ev, _ := e.adminCredentialFindings(ctx)
			if st != c.want || !strings.Contains(ev, c.mention) {
				t.Fatalf("status %s evidence %q, want %s mentioning %q", st, ev, c.want, c.mention)
			}
		})
	}
}

func TestDefaultCredentialCheckDoesNotPassWithoutAdmins(t *testing.T) {
	d := credDB(t)
	st, _, _ := NewEvaluator(&config.Config{}, d).adminCredentialFindings(context.Background())
	if st == StatusPass {
		t.Fatal("no administrator accounts was reported as a pass")
	}
}

func TestClassifyDBMode(t *testing.T) {
	for mode, want := range map[os.FileMode]CheckStatus{
		0o600: StatusPass, 0o400: StatusPass, 0o640: StatusWarn, 0o660: StatusWarn, 0o644: StatusFail, 0o666: StatusFail, 0o604: StatusFail,
	} {
		if got, _ := classifyDBMode("x.db", mode); got != want {
			t.Errorf("mode %04o: %s, want %s", mode, got, want)
		}
	}
}

// On Windows the ACL is not inspected, so the check must not claim a pass;
// with PostgreSQL there is no file to check.
func TestDBPermissionCheckIsHonestWhereItCannotLook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.db")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := NewEvaluator(&config.Config{DBPath: path}, nil)
	if st, ev, _ := e.dbFilePermissionFindings("windows"); st != StatusWarn || !strings.Contains(ev, "Not verified") {
		t.Fatalf("windows: %s %q, want a not-verified warning", st, ev)
	}
	pg := NewEvaluator(&config.Config{DBURL: "postgres://x"}, nil)
	if st, ev, _ := pg.dbFilePermissionFindings("linux"); st != StatusPass || !strings.Contains(ev, "Not applicable") {
		t.Fatalf("postgres: %s %q", st, ev)
	}
	missing := NewEvaluator(&config.Config{DBPath: filepath.Join(t.TempDir(), "nope.db")}, nil)
	if st, _, _ := missing.dbFilePermissionFindings("linux"); st == StatusPass {
		t.Fatal("an unreadable database file was reported as a pass")
	}
}

// A real file with real modes, where the OS has them (the Linux CI runner).
func TestDBPermissionCheckReadsTheFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not POSIX permissions on Windows")
	}
	path := filepath.Join(t.TempDir(), "p.db")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := NewEvaluator(&config.Config{DBPath: path}, nil)
	if st, ev, _ := e.dbFilePermissionFindings(runtime.GOOS); st != StatusPass {
		t.Fatalf("0600: %s %q", st, ev)
	}
	if err := os.WriteFile(path+"-wal", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+"-wal", 0o644); err != nil {
		t.Fatal(err)
	}
	if st, ev, _ := e.dbFilePermissionFindings(runtime.GOOS); st != StatusFail || !strings.Contains(ev, "-wal") {
		t.Fatalf("world-readable WAL: %s %q, want a failure naming it", st, ev)
	}
}

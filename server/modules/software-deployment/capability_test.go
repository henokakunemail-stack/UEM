package softwaredeployment

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, no cgo
)

// The capability gate is the only thing standing between an operator and a
// deployment that can never complete. An agent that predates a command answers
// with an error that lands in agent_commands, not deployment_tasks, so the task
// row never moves; the orphan sweep does not help either, because it only reaps
// devices that are offline. On a healthy endpoint the row would sit in
// 'dispatched' forever.
//
// So the gate has to be conservative in exactly one direction: anything it is
// not sure about must be treated as unable, never as able.
func TestDevicesWithoutCapability(t *testing.T) {
	cases := []struct {
		name    string
		caps    string // raw column value; "" means SQL NULL
		command string
		want    bool // true when the device should be reported as MISSING
	}{
		{
			name:    "agent advertises the command",
			caps:    `["ping","software.uninstall","exec.run"]`,
			command: "software.uninstall",
			want:    false,
		},
		{
			name:    "agent predates the command",
			caps:    `["ping","software.install","exec.run"]`,
			command: "software.uninstall",
			want:    true,
		},
		{
			// The failure this whole check exists for: install must not be
			// mistaken for uninstall just because one is a prefix of the other.
			name:    "install capability does not satisfy uninstall",
			caps:    `["software.install"]`,
			command: "software.uninstall",
			want:    true,
		},
		{
			// And the mirror image, which a substring test gets wrong in the
			// other direction.
			name:    "uninstall capability does not satisfy install",
			caps:    `["software.uninstall"]`,
			command: "software.install",
			want:    true,
		},
		{
			name:    "null capabilities means unknown, not capable",
			caps:    "",
			command: "software.install",
			want:    true,
		},
		{
			name:    "empty array means incapable",
			caps:    `[]`,
			command: "software.install",
			want:    true,
		},
		{
			name:    "unparseable json is treated as absent",
			caps:    `["software.install"`,
			command: "software.install",
			want:    true,
		},
		{
			name:    "a json string is not a capability list",
			caps:    `"software.install"`,
			command: "software.install",
			want:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sqlx.Open("sqlite", filepath.Join(t.TempDir(), "caps.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			mustExec(t, db, `
				CREATE TABLE devices (
					id TEXT PRIMARY KEY,
					os_name TEXT,
					capabilities TEXT
				)`)
			if tc.caps == "" {
				mustExec(t, db, `INSERT INTO devices (id, os_name, capabilities)
					VALUES ('dev1', 'windows', NULL)`)
			} else {
				mustExec(t, db, `INSERT INTO devices (id, os_name, capabilities)
					VALUES ('dev1', 'windows', ?)`, tc.caps)
			}

			repo := &Repository{db: db}
			missing, err := repo.DevicesWithoutCapability(
				context.Background(), []string{"dev1"}, tc.command)
			if err != nil {
				t.Fatalf("DevicesWithoutCapability: %v", err)
			}

			if got := len(missing) == 1; got != tc.want {
				t.Fatalf("reported missing = %v (%d devices), want %v", got, len(missing), tc.want)
			}
			if tc.want && missing[0].ID != "dev1" {
				t.Errorf("missing device id = %q, want dev1", missing[0].ID)
			}
		})
	}
}

// A mixed fleet is the normal case during a rollout, and it is the one a
// naive "if any device is missing, stop everything" check would mishandle: the
// capable endpoints should still get the task.
func TestDevicesWithoutCapabilityReportsOnlyTheIncapableOnes(t *testing.T) {
	db, err := sqlx.Open("sqlite", filepath.Join(t.TempDir(), "caps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mustExec(t, db, `
		CREATE TABLE devices (id TEXT PRIMARY KEY, os_name TEXT, capabilities TEXT);
		INSERT INTO devices (id, os_name, capabilities) VALUES
			('capable',   'windows', '["software.uninstall"]'),
			('old-agent', 'windows', '["software.install"]'),
			('no-hello',  'linux',   NULL),
			('other-os',  'macos',   '["ping"]');`)

	repo := &Repository{db: db}
	missing, err := repo.DevicesWithoutCapability(context.Background(),
		[]string{"capable", "old-agent", "no-hello", "other-os"}, "software.uninstall")
	if err != nil {
		t.Fatalf("DevicesWithoutCapability: %v", err)
	}

	got := map[string]bool{}
	for _, d := range missing {
		got[d.ID] = true
	}
	want := map[string]bool{"old-agent": true, "no-hello": true, "other-os": true}
	if len(got) != len(want) {
		t.Fatalf("missing = %v, want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("device %q should have been reported missing", id)
		}
	}
	if got["capable"] {
		t.Error("a capable device must not be reported missing")
	}
}

// An id that is not in the table at all cannot be dispatched to either, but it
// must not be reported as a capability failure: that is a different problem and
// conflating them would send an operator looking at agent versions.
func TestDevicesWithoutCapabilityIgnoresUnknownIDs(t *testing.T) {
	db, err := sqlx.Open("sqlite", filepath.Join(t.TempDir(), "caps.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mustExec(t, db, `CREATE TABLE devices (id TEXT PRIMARY KEY, os_name TEXT, capabilities TEXT)`)

	repo := &Repository{db: db}
	missing, err := repo.DevicesWithoutCapability(context.Background(), []string{"ghost"}, "software.uninstall")
	if err != nil {
		t.Fatalf("DevicesWithoutCapability: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
}

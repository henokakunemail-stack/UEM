package devicemanagement

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// TestDeleteDeviceChildTablesExist runs every purge statement against the real
// migrated schema. The list named two tables that never existed; SQLite's "no
// such table" was skipped, so nothing noticed, while PostgreSQL aborted the
// whole purge transaction on the first one.
func TestDeleteDeviceChildTablesExist(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "purge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for _, q := range devicePurgeQueries {
		if _, err := database.Exec(q, "no-such-device"); err != nil {
			t.Errorf("purge statement fails on the migrated schema: %s: %v", q, err)
		}
	}
}

// TestDeleteGroupRefusedWhileSystemPolicyTargetsIt: system_policies was missing
// from the in-use check, so a group a system policy still targeted could be
// deleted, leaving the policy aimed at nothing.
func TestDeleteGroupRefusedWhileSystemPolicyTargetsIt(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "groupuse.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	if _, err := database.Exec(`INSERT INTO device_groups (id, name, description, created_at, updated_at)
		VALUES ('g1', 'Finance', '', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO system_policies (id, name, target_type, target_id, settings_json, created_by, created_at, updated_at)
		VALUES ('p1', 'Block USB', 'group', 'g1', '{}', 'u1', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}

	label, err := newInventoryRepository(database).groupInUse(context.Background(), "g1")
	if err != nil {
		t.Fatal(err)
	}
	if label != "a system policy" {
		t.Errorf("groupInUse = %q, want \"a system policy\": deleting the group would orphan the policy", label)
	}
}

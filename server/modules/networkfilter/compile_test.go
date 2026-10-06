package networkfilter

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// A device can belong to several groups at once: device_group_members is keyed by
// (group_id, device_id), not device_id, and nothing in the console prevents adding
// one device to a finance group and a kiosk group in the same sitting.
//
// CompileEffectiveRules used to resolve that with "SELECT group_id ... LIMIT 1",
// so it picked one group arbitrarily and compiled only that group's policies. The
// other policies were enabled, listed in the console, and enforced for nobody --
// with no error anywhere, because the query succeeded and returned a shorter list
// than the operator expected.
func TestCompileCoversEveryGroupTheDeviceBelongsTo(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "multigroup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	repo := NewRepository(database)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-multi', 'PC-MULTI', 'windows', '11.0', '1.0.0', 'Jakarta', 'online', ?, ?, 'h', ?, ?)
	`, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	for _, g := range []struct{ id, name string }{
		{"grp-a", "Finance"},
		{"grp-b", "Kiosk"},
		{"grp-c", "Reception"},
	} {
		if _, err := database.Exec(
			`INSERT INTO device_groups (id, name, description, created_at, updated_at) VALUES (?, ?, '', ?, ?)`,
			g.id, g.name, now, now); err != nil {
			t.Fatal(err)
		}
	}
	// One device, three memberships. Insertion order is deliberately not alphabetical:
	// "LIMIT 1" would have returned grp-a here, so a test written against sorted
	// groups could pass by luck.
	for _, g := range []string{"grp-a", "grp-b", "grp-c"} {
		if _, err := database.Exec(
			`INSERT INTO device_group_members (group_id, device_id, added_at) VALUES (?, 'dev-multi', ?)`,
			g, now); err != nil {
			t.Fatal(err)
		}
	}

	wantPatterns := map[string]string{
		"finance-only.com":   "grp-a",
		"kiosk-only.com":     "grp-b",
		"reception-only.com": "grp-c",
	}
	for pattern, group := range wantPatterns {
		p := &FilterPolicy{
			Name:       "policy for " + pattern,
			TargetType: "group",
			TargetID:   group,
			CreatedBy:  "admin",
			IsEnabled:  true,
			Priority:   100,
		}
		if err := repo.CreatePolicy(ctx, p); err != nil {
			t.Fatalf("create policy for %s: %v", pattern, err)
		}
		if err := repo.AddRule(ctx, &FilterRule{
			PolicyID: p.ID, RuleType: "domain", Pattern: pattern, Action: "block", Category: "test",
		}); err != nil {
			t.Fatalf("add rule %s: %v", pattern, err)
		}
	}

	rules, _, err := repo.CompileEffectiveRules(ctx, "dev-multi")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	seen := make(map[string]bool, len(rules))
	for _, got := range rules {
		seen[got] = true
	}
	for pattern := range wantPatterns {
		if !seen[pattern] {
			t.Errorf("pattern %q was not compiled; a device in three groups only got %v. "+
				"Group resolution returned one group instead of all of them", pattern, rules)
		}
	}
}

// The version hash is what the device reports back and what the console compares
// against to decide whether anything changed. A device in multiple groups must
// still produce one deterministic hash -- if the group query were unordered, the
// same device would look like it needed a new policy on every reconnect.
func TestCompileIsDeterministicAcrossGroups(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "stable.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	repo := NewRepository(database)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-stable', 'PC-STABLE', 'windows', '11.0', '1.0.0', 'Jakarta', 'online', ?, ?, 'h', ?, ?)
	`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{"grp-x", "grp-y"} {
		if _, err := database.Exec(
			`INSERT INTO device_groups (id, name, description, created_at, updated_at) VALUES (?, ?, '', ?, ?)`,
			g, g, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(
			`INSERT INTO device_group_members (group_id, device_id, added_at) VALUES (?, 'dev-stable', ?)`,
			g, now); err != nil {
			t.Fatal(err)
		}
		p := &FilterPolicy{Name: "p " + g, TargetType: "group", TargetID: g, CreatedBy: "admin", IsEnabled: true, Priority: 100}
		if err := repo.CreatePolicy(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := repo.AddRule(ctx, &FilterRule{
			PolicyID: p.ID, RuleType: "domain", Pattern: g + ".example.com", Action: "block", Category: "test",
		}); err != nil {
			t.Fatal(err)
		}
	}

	_, first, err := repo.CompileEffectiveRules(ctx, "dev-stable")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_, again, err := repo.CompileEffectiveRules(ctx, "dev-stable")
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatalf("compile %d produced version %q, first was %q: an unstable version "+
				"hash makes every reconnect look like a policy change", i, again, first)
		}
	}
}

// A device in no group at all is the common case, and sqlx.In cannot bind an empty
// slice -- so the group branch has to be skipped entirely rather than run with no
// arguments. This is the case that broke if the skip was forgotten.
func TestCompileForDeviceWithNoGroups(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "nogroups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	repo := NewRepository(database)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-solo', 'PC-SOLO', 'windows', '11.0', '1.0.0', 'Jakarta', 'online', ?, ?, 'h', ?, ?)
	`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`INSERT INTO device_groups (id, name, description, created_at, updated_at) VALUES ('grp-other', 'Other', '', ?, ?)`,
		now, now); err != nil {
		t.Fatal(err)
	}

	global := &FilterPolicy{Name: "fleet-wide", TargetType: "all", CreatedBy: "admin", IsEnabled: true, Priority: 10}
	if err := repo.CreatePolicy(ctx, global); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddRule(ctx, &FilterRule{
		PolicyID: global.ID, RuleType: "domain", Pattern: "fleet-wide.com", Action: "block", Category: "test",
	}); err != nil {
		t.Fatal(err)
	}

	grouped := &FilterPolicy{Name: "someone elses group", TargetType: "group", TargetID: "grp-other", CreatedBy: "admin", IsEnabled: true, Priority: 10}
	if err := repo.CreatePolicy(ctx, grouped); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddRule(ctx, &FilterRule{
		PolicyID: grouped.ID, RuleType: "domain", Pattern: "not-mine.com", Action: "block", Category: "test",
	}); err != nil {
		t.Fatal(err)
	}

	rules, _, err := repo.CompileEffectiveRules(ctx, "dev-solo")
	if err != nil {
		t.Fatalf("compile for a device in no group: %v", err)
	}
	if len(rules) != 1 || rules[0] != "fleet-wide.com" {
		t.Fatalf("got %v, want exactly [fleet-wide.com]: a device in no group still needs "+
			"the fleet-wide policies and must not inherit another group's", rules)
	}
}

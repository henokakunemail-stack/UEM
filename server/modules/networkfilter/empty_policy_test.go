package networkfilter

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

func emptyPolicyRepo(t *testing.T) *Repository {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	now := time.Now().UTC()
	if _, err := database.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, status, enrolled_at, last_seen_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-1', 'PC-1', 'windows', '11.0', '1.0.0', 'Jakarta', 'online', ?, ?, 'h', ?, ?)
	`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	return NewRepository(database)
}

// TestCompileIgnoresPolicyWithNoRules: a device-scoped whitelist saved before its
// first rule outranks the fleet blacklist. It used to decide the mode anyway,
// compile to "whitelist, allow nothing", and the agent removed every block.
func TestCompileIgnoresPolicyWithNoRules(t *testing.T) {
	repo := emptyPolicyRepo(t)
	ctx := context.Background()

	fleet := &FilterPolicy{Name: "Fleet blacklist", Mode: "blacklist", TargetType: "all",
		CreatedBy: "admin", IsEnabled: true, Priority: 100}
	if err := repo.CreatePolicy(ctx, fleet); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddRule(ctx, &FilterRule{PolicyID: fleet.ID, RuleType: "domain",
		Pattern: "gambling.example", Action: "block", Category: "custom"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreatePolicy(ctx, &FilterPolicy{Name: "Kiosk whitelist (not filled yet)",
		Mode: "whitelist", TargetType: "device", TargetID: "dev-1",
		CreatedBy: "admin", IsEnabled: true, Priority: 1}); err != nil {
		t.Fatal(err)
	}

	compiled, err := repo.CompileEffectiveRules(ctx, "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Mode != "blacklist" || !slices.Equal(compiled.BlockedDomains, []string{"gambling.example"}) {
		t.Errorf("compiled = mode %q blocked %v allowed %v, want the fleet blacklist: an empty "+
			"whitelist must not switch the device to allow-nothing and drop every block",
			compiled.Mode, compiled.BlockedDomains, compiled.AllowedDomains)
	}
}

// TestCompileAllEmptyIsCleanState: when every policy aimed at the device is
// empty the result is the same clean state as having no policy at all.
func TestCompileAllEmptyIsCleanState(t *testing.T) {
	repo := emptyPolicyRepo(t)
	ctx := context.Background()
	if err := repo.CreatePolicy(ctx, &FilterPolicy{Name: "Empty whitelist", Mode: "whitelist",
		TargetType: "all", CreatedBy: "admin", IsEnabled: true, Priority: 100}); err != nil {
		t.Fatal(err)
	}

	compiled, err := repo.CompileEffectiveRules(ctx, "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Mode != "blacklist" || len(compiled.BlockedDomains) != 0 ||
		len(compiled.AllowedDomains) != 0 || compiled.Version != "" {
		t.Errorf("compiled = %+v, want the clean no-policy state", compiled)
	}
}

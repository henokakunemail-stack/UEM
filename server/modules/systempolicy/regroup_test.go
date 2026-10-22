package systempolicy

import (
	"context"
	"testing"

	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// recordingHub pretends every device is online and keeps what was sent.
type recordingHub struct{ sent map[string]int }

func (h *recordingHub) Online(string) bool { return true }
func (h *recordingHub) SendTo(id string, _ []byte) bool {
	h.sent[id]++
	return true
}

// TestDevicesRegroupedDispatches: a device moved into a group that a USB-block
// policy targets used to keep its old policy, because membership changes only
// touched device_group_members. The observer must re-send the recompiled
// policy and record it as pending at the new version.
func TestDevicesRegroupedDispatches(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	if _, err := repo.db.Exec(`INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status, last_seen_at, enrolled_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-1', 'PC-1', 'Windows', '11', '1.5.4', 'online', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 'h', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`INSERT INTO device_groups (id, name, created_at, updated_at)
		VALUES ('g1', 'Finance', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, &SystemPolicy{
		Name: "Block USB", TargetType: "group", TargetID: "g1", IsEnabled: true, Priority: 100,
		CreatedBy: "admin", Settings: legacy(PolicySettings{USBStorageMode: USBStorageBlock}),
	}); err != nil {
		t.Fatal(err)
	}
	// The device joins the group after the policy exists, exactly as the
	// inventory module's addMembers would record it.
	if _, err := repo.db.Exec(`INSERT INTO device_group_members (group_id, device_id, added_at, added_by)
		VALUES ('g1', 'dev-1', CURRENT_TIMESTAMP, 'admin')`); err != nil {
		t.Fatal(err)
	}

	hub := &recordingHub{sent: map[string]int{}}
	h := NewHandler(repo, hub, devicemgmt.NewRepository(repo.db), nopAudit{}, nil)
	h.DevicesRegrouped(ctx, []string{"dev-1"})

	if hub.sent["dev-1"] != 1 {
		t.Fatalf("policy sent %d times after the device joined the group, want 1", hub.sent["dev-1"])
	}
	compiled, err := repo.CompileEffectivePolicy(ctx, "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	state, err := repo.GetDeviceState(ctx, "dev-1")
	if err != nil || state == nil {
		t.Fatalf("no state recorded after the re-dispatch: %v", err)
	}
	if state.PolicyVersion != compiled.Version || state.Status != "pending" {
		t.Errorf("state = %s@%s, want pending@%s", state.Status, state.PolicyVersion, compiled.Version)
	}
}

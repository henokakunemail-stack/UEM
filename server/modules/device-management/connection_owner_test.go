package devicemanagement

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// An agent that drops from node A and reconnects to node B before A finishes
// cleaning up is owned by B. A's late cleanup must not mark it offline: the
// console would show a connected agent as offline until its next heartbeat.
func TestALateDisconnectOnTheOldNodeDoesNotMarkTheDeviceOffline(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "owner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo := NewRepository(database)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := database.Exec(`INSERT INTO devices (id, hostname, os_name, status, enrolled_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-1','h','windows','online',?,'x',?,?)`, now, now, now); err != nil {
		t.Fatal(err)
	}

	if err := repo.ClaimConnection(ctx, "dev-1", "node-a", now); err != nil {
		t.Fatal(err)
	}
	// The agent reconnects to B before A's disconnect cleanup runs.
	if err := repo.ClaimConnection(ctx, "dev-1", "node-b", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	released, err := repo.ReleaseConnection(ctx, "dev-1", "node-a", now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if released {
		t.Fatal("node A released a device node B owns")
	}
	d, err := repo.GetByID(ctx, "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusOnline || d.ConnectedNode == nil || *d.ConnectedNode != "node-b" {
		t.Fatalf("after A's late cleanup: status=%s owner=%v, want online and node-b", d.Status, d.ConnectedNode)
	}

	// B's own disconnect does mark it offline.
	if released, err := repo.ReleaseConnection(ctx, "dev-1", "node-b", now.Add(3*time.Second)); err != nil || !released {
		t.Fatalf("the owning node could not release (released=%v err=%v)", released, err)
	}
	d, _ = repo.GetByID(ctx, "dev-1")
	if d.Status != StatusOffline || d.ConnectedNode != nil {
		t.Fatalf("after the owner's disconnect: status=%s owner=%v", d.Status, d.ConnectedNode)
	}
}

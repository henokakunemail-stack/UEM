package agentupdate

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// macDevice adds an online macOS device whose inventory says arm64, the way a
// real Apple Silicon agent reports itself: os_name "macos", hw.machine "arm64".
func (f *queueFixture) macDevice(t *testing.T, id string) {
	t.Helper()
	f.seedDevice(t, id, "macos", "online")
	if _, err := f.db.Exec(`
		INSERT INTO device_inventory (id, device_id, hw, software, os_detail, collected_at, updated_at)
		VALUES (?, ?, '{}', '[]', '{"architecture":"arm64"}', ?, ?)`,
		"inv-"+id, id, time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

// The console's upload form sent os_name "darwin" for macOS while every macOS
// agent reports "macos", and the release lookup compared the two exactly. A
// macOS release was therefore never found for a macOS device: dispatch said
// "no active release" and a campaign failed every Mac with no_release.
func TestAReleaseUploadedAsDarwinUpdatesAMacOSDevice(t *testing.T) {
	f := newQueueFixture(t, true)
	f.macDevice(t, "dev-mac")

	uploadReleaseFixture(t, f.r, "2.0.0", "darwin", "arm64", []byte("mach-o bytes"))

	rec := f.dispatch(t, "dev-mac", "2.0.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch to a macOS device = %d %s, want 200", rec.Code, rec.Body.String())
	}

	var stored string
	if err := f.db.Get(&stored, `SELECT os_name FROM agent_releases WHERE version = '2.0.0'`); err != nil {
		t.Fatal(err)
	}
	if stored != "macos" {
		t.Errorf("release stored with os_name %q, want %q (what the agent reports)", stored, "macos")
	}
}

// A release row written before uploads were normalised still says "darwin";
// it has to keep resolving for a macOS device rather than strand the rollout.
func TestALegacyDarwinReleaseRowStillResolves(t *testing.T) {
	f := newQueueFixture(t, true)
	if _, err := f.db.Exec(`
		INSERT INTO agent_releases (id, version, os_name, arch, file_path, file_size,
			sha256_checksum, changelog, is_active, uploaded_by, created_at)
		VALUES ('rel-mac', '3.0.0', 'darwin', 'arm64', 'x', 1, 'abc', '', 1, 'admin', ?)`,
		time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	rel, err := f.repo.GetActiveReleaseForDevice(context.Background(), "3.0.0", "macos", "arm64")
	if err != nil {
		t.Fatalf("legacy darwin row not found for a macos device: %v", err)
	}
	if rel.ID != "rel-mac" {
		t.Errorf("got release %s, want rel-mac", rel.ID)
	}
}

func TestNormalizeOSName(t *testing.T) {
	for in, want := range map[string]string{
		"darwin": "macos", "macOS": "macos", " MacOS ": "macos", "osx": "macos",
		"Windows": "windows", "win": "windows", "linux": "linux",
	} {
		if got := NormalizeOSName(in); got != want {
			t.Errorf("NormalizeOSName(%q) = %q, want %q", in, got, want)
		}
	}
}

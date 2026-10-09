package agentupdate

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

func TestGetDeviceArch(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "arch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	// Default fallback when device has no inventory
	arch := repo.GetDeviceArch(ctx, "dev-unknown")
	if arch != "amd64" {
		t.Errorf("GetDeviceArch for unknown device = %q, want %q", arch, "amd64")
	}

	// Insert inventory with ARM64 architecture
	_, err = database.Exec(`
		INSERT INTO devices (id, hostname, os_name, status, device_secret_hash, enrolled_at, created_at, updated_at)
		VALUES ('dev-arm', 'mac-m1', 'darwin', 'online', 'hash', datetime('now'), datetime('now'), datetime('now'))
	`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`
		INSERT INTO device_inventory (id, device_id, hw, software, os_detail, collected_at, updated_at)
		VALUES ('inv-1', 'dev-arm', '{}', '[]', '{"architecture":"arm64"}', datetime('now'), datetime('now'))
	`)
	if err != nil {
		t.Fatal(err)
	}

	arch = repo.GetDeviceArch(ctx, "dev-arm")
	if arch != "arm64" {
		t.Errorf("GetDeviceArch for arm device = %q, want %q", arch, "arm64")
	}
}

func TestNormalizeArch(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"arm64", "arm64"},
		{"aarch64", "arm64"},
		{"AMD64", "amd64"},
		{"x86_64", "amd64"},
		{"64-bit", "amd64"},
		{"", "amd64"},
	}

	for _, tc := range cases {
		got := normalizeArch(tc.input)
		if got != tc.want {
			t.Errorf("normalizeArch(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

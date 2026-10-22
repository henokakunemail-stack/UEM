package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The download used to be staged in os.TempDir() and then renamed over the
// executable. A rename cannot cross filesystems, and the temp directory is on
// a different one often enough to matter: /tmp is a tmpfs on many Linux
// distributions, and C:\Windows\Temp is on C: when the agent lives on D:.
// Every such update ended in rollback. The temp directory is made unusable
// here, which is the same failure from the engine's point of view: an update
// must not depend on it at all.
func TestAnUpdateDoesNotDependOnTheSystemTempDirectory(t *testing.T) {
	// A real executable for this platform, so the post-swap format check
	// passes: the test binary itself.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	release, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	execPath := filepath.Join(dir, "endpoint-agent")
	if err := os.WriteFile(execPath, []byte("the running agent"), 0o755); err != nil {
		t.Fatal(err)
	}

	missing := filepath.Join(dir, "no-such-temp-dir")
	t.Setenv("TMPDIR", missing)
	t.Setenv("TMP", missing)
	t.Setenv("TEMP", missing)

	srv := &blobServer{content: string(release)}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	sum := sha256.Sum256(release)
	e := NewEngine(ts.URL, "dev-1", "secret")
	e.SetExecutablePath(execPath)
	if err := e.ApplyUpdate(context.Background(), UpdateParams{
		TaskID:         "task-staging",
		TargetVersion:  "2.0.0",
		DownloadURL:    "/blob",
		SHA256Checksum: hex.EncodeToString(sum[:]),
	}); err != nil {
		t.Fatalf("update failed with the system temp directory unusable: %v (reports: %v)", err, srv.all())
	}
	if !srv.saw(t, "success") {
		t.Errorf("success was not reported (reports: %v)", srv.all())
	}

	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, release) {
		t.Error("the executable was not replaced by the release")
	}
	entries, _ := os.ReadDir(dir)
	for _, ent := range entries {
		if filepath.Ext(ent.Name()) == ".tmp" {
			t.Errorf("staging file %s was left behind", ent.Name())
		}
	}
}

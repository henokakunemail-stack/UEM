package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// servingBlob serves `content` at /blob and records every progress report the
// engine sends, so a test can assert what the fleet was told.
type blobServer struct {
	content string
	mu      sync.Mutex
	reports []string
}

func (s *blobServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(s.content))
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.reports = append(s.reports, body["status"])
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
}

func (s *blobServer) saw(t *testing.T, status string) bool {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.reports {
		if r == status {
			return true
		}
	}
	return false
}

func (s *blobServer) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reports...)
}

// runApply writes `content` as the release, runs a full ApplyUpdate against a
// throwaway "current" binary, and returns the outcome plus the server's view.
func runApply(t *testing.T, content string) (srv *blobServer, err error) {
	t.Helper()

	srv = &blobServer{content: content}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	sum := sha256.Sum256([]byte(content))
	dir := t.TempDir()
	execPath := filepath.Join(dir, "agent.exe")
	// The "running" agent. Also not a real program, which is fine: nothing
	// ever executes either one, which is the point.
	if err := os.WriteFile(execPath, []byte("pretend i am a running agent"), 0755); err != nil {
		t.Fatal(err)
	}

	e := NewEngine(ts.URL, "dev-1", "secret")
	e.SetExecutablePath(execPath)
	return srv, e.ApplyUpdate(context.Background(), UpdateParams{
		TaskID:         "task-1",
		TargetVersion:  "2.0.0",
		DownloadURL:    "/blob",
		SHA256Checksum: hex.EncodeToString(sum[:]),
	})
}

// TestAnUnbootableReleaseIsNotReportedAsASuccessfulUpdate is the regression test
// for a health check that could not fail.
//
// The post-swap check was os.Stat plus a non-zero size. That proves the rename
// landed and something is at the path. Everything else was assumed: the bytes
// were never looked at. So a text file, a truncated upload that happened to
// match its recorded checksum, or any blob at all would be swapped in, reported
// to the server as "success", and have the device's agent_version rewritten.
//
// The damage is deferred. The running agent keeps its own image in memory, so
// it works fine until the machine restarts, and then it cannot start. The last
// thing the audit trail shows for that device is a successful upgrade.
func TestAnUnbootableReleaseIsNotReportedAsASuccessfulUpdate(t *testing.T) {
	srv, err := runApply(t, "this is not a program; it will not boot\n")
	if err == nil {
		t.Fatal("ApplyUpdate reported success for a file that is not a program")
	}
	if srv.saw(t, "success") {
		t.Fatalf("the server was told \"success\" (reports: %v): the fleet's version "+
			"inventory now claims this device runs a binary that cannot start", srv.all())
	}
	if !srv.saw(t, "rollback") {
		t.Errorf("no rollback was reported (reports: %v); the device is left with a "+
			"broken binary and no record of why", srv.all())
	}
}

// TestTheOriginalBinaryIsRestoredAfterAFailedHealthCheck: rejecting the file is
// half the fix. If the swap is not undone, the device is running fine right now
// and dead at the next restart, and the rollback report is a claim rather than
// a fact.
func TestTheOriginalBinaryIsRestoredAfterAFailedHealthCheck(t *testing.T) {
	const original = "pretend i am a running agent"
	_, _ = runApply(t, "not a program")

	// Re-run inline so the path is observable.
	srv := &blobServer{content: "not a program"}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "agent.exe")
	if err := os.WriteFile(execPath, []byte(original), 0755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("not a program"))
	e := NewEngine(ts.URL, "dev-1", "secret")
	e.SetExecutablePath(execPath)
	if err := e.ApplyUpdate(context.Background(), UpdateParams{
		TaskID: "task-1", TargetVersion: "2.0.0", DownloadURL: "/blob",
		SHA256Checksum: hex.EncodeToString(sum[:]),
	}); err == nil {
		t.Fatal("ApplyUpdate reported success for a file that is not a program")
	}

	restored, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != original {
		t.Errorf("executable is %q, want the original %q: the device is running from "+
			"memory now and would fail to start at the next reboot",
			restored, original)
	}
}

// TestAChecksumMismatchStillRollsBack keeps the digest check in place. The
// format check is additional; it must not have displaced it.
//
// Note the status is "failed", not "rollback", and that is deliberate rather
// than an oversight in the assertion: a digest mismatch is detected at step 2,
// before step 3 renames anything. Nothing was swapped, so there is nothing to
// roll back, and reporting "rollback" would put a non-event in the audit trail
// next to the real ones. The behaviour worth pinning is that the download never
// reaches the executable at all -- the original binary must come out of this
// untouched, which is a stronger claim than any rollback.
func TestAChecksumMismatchStillRollsBack(t *testing.T) {
	const original = "original binary"
	srv := &blobServer{content: "not a program"}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "agent.exe")
	if err := os.WriteFile(execPath, []byte(original), 0755); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(ts.URL, "dev-1", "secret")
	e.SetExecutablePath(execPath)
	// A checksum that is deliberately wrong for the content served.
	err := e.ApplyUpdate(context.Background(), UpdateParams{
		TaskID: "task-1", TargetVersion: "2.0.0", DownloadURL: "/blob",
		SHA256Checksum: hex.EncodeToString(make([]byte, 32)),
	})
	if err == nil {
		t.Fatal("ApplyUpdate accepted a download whose checksum did not match")
	}

	// The digest check must not have been replaced or reordered after the
	// format check: "swapping" is reported only once the digest is confirmed.
	for _, r := range srv.all() {
		if r == "swapping" || r == "success" {
			t.Fatalf("reports = %v: the executable was touched by a download that "+
				"failed its digest check", srv.all())
		}
	}
	if !srv.saw(t, "failed") {
		t.Errorf("reports = %v, want a failure recorded", srv.all())
	}

	after, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Errorf("executable is %q, want the untouched original %q", after, original)
	}
}

// TestAZeroLengthFileIsRejectedDirectly: the check the old code did perform --
// a non-empty file -- is now a floor rather than the whole test, and it is
// worth pinning on its own.
func TestAZeroLengthFileIsRejectedDirectly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.exe")
	if err := os.WriteFile(p, nil, 0755); err != nil {
		t.Fatal(err)
	}
	if err := verifySwappedBinary(p); err == nil {
		t.Error("a zero-length file was accepted as an executable")
	}
}

func TestVerifySwappedBinaryTruncated(t *testing.T) {
	p := filepath.Join(t.TempDir(), "short.bin")
	if err := os.WriteFile(p, []byte{0x7f, 'E'}, 0755); err != nil {
		t.Fatal(err)
	}
	if err := verifySwappedBinary(p); err == nil {
		t.Error("a truncated file < 4 bytes was accepted")
	}
}

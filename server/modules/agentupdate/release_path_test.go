package agentupdate

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
)

type noopAuditor struct{}

func (noopAuditor) Log(context.Context, string, string, string, string, map[string]string) error {
	return nil
}

// TestReleasePathStaysInsideTheStorageDirectory is the regression test for an
// arbitrary file write.
//
// version, os_name and arch are operator-supplied form fields that used to be
// interpolated straight into filepath.Join. Join calls Clean, which resolves
// "..", so these versions each escaped the release directory while the upload
// still answered 201:
//
//	"..\..\Windows\System32"  ->  one level above data/agent-releases
//	"..\..\startup"           ->  outside storage entirely
//
// The write primitive here composes with the rest of the system: whatever lands
// is what the agent-download route later serves to every device in the fleet.
// The route is admin-gated, so this is not privilege escalation, but it is a
// write outside the directory the operator granted, reported as success.
func TestReleasePathStaysInsideTheStorageDirectory(t *testing.T) {
	dir := t.TempDir()

	hostile := []string{
		"..",
		"..\\..",
		"..\\..\\startup",
		"..\\..\\Windows\\System32",
		"../..",
		"../../etc/cron.d",
		"/etc/passwd",
		`C:\Windows\System32\evil`,
		".",
	}
	for _, v := range hostile {
		t.Run(v, func(t *testing.T) {
			for _, field := range []string{"version", "os_name", "arch"} {
				args := map[string]string{"version": "1.2.3", "os_name": "windows", "arch": "x64"}
				args[field] = v
				got, err := releasePath(dir, args["version"], args["os_name"], args["arch"], "agent.exe")
				if err == nil {
					t.Errorf("%s=%q was accepted and resolved to %q, which is outside %q",
						field, v, got, dir)
				}
			}
		})
	}
}

// TestReleasePathRejectsATraversingMultipartFilename covers the other side of
// the same join: the filename the client put in the multipart header is just as
// attacker-controlled as the form fields.
func TestReleasePathRejectsATraversingMultipartFilename(t *testing.T) {
	dir := t.TempDir()
	got, err := releasePath(dir, "1.2.3", "windows", "x64", "..\\..\\evil.exe")
	if err != nil {
		// filepath.Base reduces it, so this is the correct outcome.
		if strings.Contains(got, "..") {
			t.Errorf("resolved path %q still contains a relative segment", got)
		}
		return
	}
	if strings.Contains(got, "..") {
		t.Errorf("accepted a traversing multipart filename: %q", got)
	}
	if filepath.Dir(got) != filepath.Clean(dir) {
		t.Errorf("resolved path %q escaped %q", got, dir)
	}
}

// TestReleasePathAcceptsRealVersions makes sure the rejection above did not turn
// into a rejection of every upload. Dots are the normal case; a rule that
// refused them would break releases rather than protect them.
func TestReleasePathAcceptsRealVersions(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []string{"1.2.3", "2.0.0-rc1", "v1.4.9", "2026.09.28"} {
		got, err := releasePath(dir, v, "windows", "amd64", "agent.exe")
		if err != nil {
			t.Errorf("version %q was rejected: %v", v, err)
			continue
		}
		if filepath.Dir(got) != filepath.Clean(dir) {
			t.Errorf("version %q resolved to %q, outside %q", v, got, dir)
		}
		if filepath.Base(got) != v+"_windows_amd64_agent.exe" {
			t.Errorf("version %q produced %q; the console shows this name and the "+
				"download route matches on it", v, filepath.Base(got))
		}
	}
}

// TestUploadReleaseRejectsAHopefulVersionAndWritesNothing checks the behaviour
// an operator sees, not just the helper: a rejected upload answers 400 and
// leaves no file anywhere under the storage root.
func TestUploadReleaseRejectsAHopefulVersionAndWritesNothing(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "data", "agent-releases")
	outside := filepath.Join(root, "startup")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}

	d, err := db.Open(filepath.Join(root, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	h := NewHandler(NewRepository(d), transport.NewHub(), nil, noopAuditor{}, storeDir,
		func(n http.Handler) http.Handler { return n })

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField("version", `..\..\startup`)
	_ = mw.WriteField("os_name", "windows")
	_ = mw.WriteField("arch", "x64")
	fw, _ := mw.CreateFormFile("file", "agent.exe")
	_, _ = fw.Write([]byte("PAYLOAD"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/api/agent-updates/releases", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.handleUploadRelease(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; a traversing version was accepted", rec.Code)
	}

	// Nothing may exist outside the storage directory.
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the upload escaped the storage directory: %v", names)
	}
}

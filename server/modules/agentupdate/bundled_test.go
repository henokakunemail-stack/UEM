package agentupdate

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildGoBinary links a tiny Go program for goos/goarch, stamped the way the
// release build stamps the agent, so the tests read real build information
// rather than a fixture that only looks like one.
func buildGoBinary(t *testing.T, goos, goarch, version string) []byte {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module stampedagent\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "bin")
	args := []string{"build", "-o", out}
	if version != "" {
		args = append(args, "-ldflags", "-s -w -X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version="+version)
	}
	cmd := exec.Command(goTool, append(args, ".")...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0", "GOFLAGS=")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s/%s: %v\n%s", goos, goarch, err, msg)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *queueFixture) getJSON(t *testing.T, method, path, body string, into any) int {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body == "" {
		req.ContentLength = 0
	}
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	if into != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return rec.Code
}

type importResult struct {
	Imported []AgentReleaseDTO `json:"imported"`
	Skipped  []BundledAgent    `json:"skipped"`
}

// An enrolled agent could only be moved onto the agents a server update
// shipped by downloading each file from the release page and uploading it
// again with its version retyped. The bundle is now registered from the
// console, with platform and version read out of the binaries.
func TestBundledAgentsAreImportedWithTheirOwnPlatformAndVersion(t *testing.T) {
	f := newQueueFixture(t, true)
	bundle := t.TempDir()
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(bundle, name), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("agent-darwin-arm64", buildGoBinary(t, "darwin", "arm64", "2.1.0"))
	write("agent-windows-amd64.exe", buildGoBinary(t, "windows", "amd64", "2.1.0"))
	// Named for one platform, built for another: never registered.
	write("agent-linux-arm64", buildGoBinary(t, "linux", "amd64", "2.1.0"))
	write("README.txt", []byte("not an agent"))
	f.h.WithBundledAgents("2.1.0", bundle)

	var list []BundledAgent
	if code := f.getJSON(t, http.MethodGet, "/api/agent-updates/bundled", "", &list); code != http.StatusOK {
		t.Fatalf("list bundled = %d", code)
	}
	if len(list) != 3 {
		t.Fatalf("bundle lists %d agents (%+v), want 3", len(list), list)
	}

	var res importResult
	if code := f.getJSON(t, http.MethodPost, "/api/agent-updates/bundled/import", "", &res); code != http.StatusOK {
		t.Fatalf("import = %d", code)
	}
	if len(res.Imported) != 2 || len(res.Skipped) != 1 || res.Skipped[0].File != "agent-linux-arm64" {
		t.Fatalf("imported %+v, skipped %+v; want darwin and windows imported, the mislabelled linux file skipped", res.Imported, res.Skipped)
	}
	for _, rel := range res.Imported {
		if rel.Version != "2.1.0" {
			t.Errorf("%s/%s imported as version %q, want the stamped 2.1.0", rel.OSName, rel.Arch, rel.Version)
		}
	}

	// The macOS build lands where a macOS device looks for it.
	f.macDevice(t, "dev-mac")
	if rec := f.dispatch(t, "dev-mac", "2.1.0"); rec.Code != http.StatusOK {
		t.Fatalf("dispatch of the imported macOS agent = %d %s", rec.Code, rec.Body.String())
	}

	// A second import finds nothing new to register.
	res = importResult{}
	f.getJSON(t, http.MethodPost, "/api/agent-updates/bundled/import", "", &res)
	if len(res.Imported) != 0 {
		t.Errorf("second import registered %d releases again", len(res.Imported))
	}
}

// Only names the scan produced are importable; a path in the request is not
// a way to register an arbitrary file on the server.
func TestBundledImportRefusesNamesOutsideTheBundle(t *testing.T) {
	f := newQueueFixture(t, true)
	f.h.WithBundledAgents("2.1.0", t.TempDir())
	var res importResult
	f.getJSON(t, http.MethodPost, "/api/agent-updates/bundled/import", `{"files":["../../etc/passwd"]}`, &res)
	if len(res.Imported) != 0 || len(res.Skipped) != 1 {
		t.Fatalf("imported %+v skipped %+v", res.Imported, res.Skipped)
	}
}

func uploadRaw(t *testing.T, f *queueFixture, version, osName, arch string, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	_ = w.WriteField("version", version)
	_ = w.WriteField("os_name", osName)
	_ = w.WriteField("arch", arch)
	fw, _ := w.CreateFormFile("file", "agent.bin")
	_, _ = fw.Write(payload)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/agent-updates/releases", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	return rec
}

// A binary built for another platform, or stamped with another version, was
// accepted as whatever the form said. The first fails every device's format
// check after the download; the second reconnects reporting its real version,
// so the device never reaches the campaign target and the rollout never ends.
func TestAnUploadWhoseBinaryContradictsTheFormIsRefused(t *testing.T) {
	f := newQueueFixture(t, true)

	linux := buildGoBinary(t, "linux", "amd64", "2.1.0")
	if rec := uploadRaw(t, f, "2.1.0", "windows", "amd64", linux); rec.Code != http.StatusBadRequest {
		t.Errorf("linux binary uploaded as windows = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if rec := uploadRaw(t, f, "2.2.0", "linux", "amd64", linux); rec.Code != http.StatusBadRequest {
		t.Errorf("2.1.0 binary uploaded as 2.2.0 = %d %s, want 400", rec.Code, rec.Body.String())
	}
	var n int
	if err := f.db.Get(&n, `SELECT COUNT(*) FROM agent_releases WHERE version IN ('2.1.0', '2.2.0')`); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d refused uploads were still recorded", n)
	}

	// The form's "v" is the operator's spelling, not a different version.
	rec := uploadRaw(t, f, "v2.1.0", "linux", "x86_64", linux)
	if rec.Code != http.StatusCreated {
		t.Fatalf("matching upload = %d %s, want 201", rec.Code, rec.Body.String())
	}
	var rel AgentReleaseDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &rel)
	if rel.Version != "2.1.0" || rel.Arch != "amd64" {
		t.Errorf("stored as %s %s, want 2.1.0 amd64", rel.Version, rel.Arch)
	}
}

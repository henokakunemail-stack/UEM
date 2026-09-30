package softwaredeployment

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// packageFixture is the full schema the package and deployment paths touch:
// software_packages and software_deployments, plus the devices and group
// tables ResolveTargetDevices reads, plus deployment_tasks for the drill-down.
type packageFixture struct {
	handler *Handler
	router  *chi.Mux
	db      *sqlx.DB
	store   string
	audit   *packageAudit
}

// packageAudit keeps what was logged so a test can assert on the trail rather
// than only on the HTTP status.
type packageAudit struct{ actions []string }

func (a *packageAudit) Log(_ context.Context, _, _, action, _ string, _ map[string]string) error {
	a.actions = append(a.actions, action)
	return nil
}

func (a *packageAudit) saw(action string) bool {
	for _, got := range a.actions {
		if got == action {
			return true
		}
	}
	return false
}

// liveHub says every device is online and accepts whatever it is handed.
type liveHub struct{ online map[string]bool }

func (h *liveHub) Online(deviceID string) bool {
	if h.online == nil {
		return true
	}
	return h.online[deviceID]
}

func (h *liveHub) SendTo(string, []byte) bool { return true }

func newPackageFixture(t *testing.T, hub HubDispatcher) *packageFixture {
	t.Helper()

	// A file database with the production DSN, not ":memory:".
	//
	// ":memory:" gives every connection in a pool its own private database, so a
	// handler that lands on a different connection than the fixture's INSERTs
	// sees an empty one. That is what made this fixture fail intermittently with
	// a 404 from downloadPackage: the package row was written, and the read did
	// not find it. db.DSNForPath also keeps busy_timeout in play, which turns the
	// write contention into a wait rather than SQLITE_BUSY. Both failures are
	// documented in terminal_dispatch_test.go, where the same fix was needed.
	d, err := sqlx.Open("sqlite", db.DSNForPath(filepath.Join(t.TempDir(), "packages.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	d.MustExec(`CREATE TABLE software_packages (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, version TEXT NOT NULL,
		os_target TEXT NOT NULL, package_type TEXT NOT NULL, file_name TEXT NOT NULL,
		file_size INTEGER NOT NULL DEFAULT 0, sha256 TEXT NOT NULL DEFAULT '',
		storage_path TEXT NOT NULL DEFAULT '', install_args TEXT NOT NULL DEFAULT '',
		uninstall_args TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL);`)
	d.MustExec(`CREATE TABLE software_deployments (
		id TEXT PRIMARY KEY, package_id TEXT NOT NULL, action TEXT NOT NULL,
		name TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL DEFAULT '',
		created_by TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'running',
		created_at DATETIME NOT NULL, completed_at DATETIME);`)
	d.MustExec(`CREATE TABLE deployment_tasks (
		id TEXT PRIMARY KEY, deployment_id TEXT NOT NULL, package_id TEXT NOT NULL,
		device_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
		args TEXT NOT NULL DEFAULT '', exit_code INTEGER, output_log TEXT,
		error_message TEXT, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL,
		completed_at DATETIME);`)
	d.MustExec(`CREATE TABLE devices (
		id TEXT PRIMARY KEY, hostname TEXT NOT NULL DEFAULT '',
		os_name TEXT NOT NULL DEFAULT '', site TEXT, status TEXT NOT NULL DEFAULT 'online',
		retired_at DATETIME, capabilities TEXT);`)
	d.MustExec(`CREATE TABLE device_group_members (device_id TEXT NOT NULL, group_id TEXT NOT NULL);`)
	// All three packages advertise software.install, so a deployment test only
	// fails on the capability gate when it is that gate being tested.
	d.MustExec(`INSERT INTO devices (id, hostname, os_name, capabilities) VALUES
		('dev-1','ALPHA-PC','windows','["software.install","software.uninstall"]'),
		('dev-2','BETA-PC','windows','["software.install","software.uninstall"]'),
		('dev-old','RETIRED-PC','windows','["software.install"]');`)
	d.MustExec(`UPDATE devices SET retired_at = ? WHERE id = 'dev-old'`, time.Now().UTC())
	d.MustExec(`INSERT INTO device_group_members (device_id, group_id) VALUES
		('dev-1','grp-1'), ('dev-2','grp-1');`)

	auditor := &packageAudit{}
	store := t.TempDir()
	h := NewHandler(&Repository{db: d}, hub, auditor, store,
		func(n http.Handler) http.Handler { return n },
		lookup{devices: map[string]devicemgmt.Device{
			devicemgmt.HashToken("agent-secret"): {ID: "dev-1"},
		}})

	r := chi.NewRouter()
	r.Get("/api/software/packages", h.listPackages)
	r.Get("/api/software/packages/{id}", h.getPackage)
	r.Post("/api/software/packages", h.uploadPackage)
	r.Delete("/api/software/packages/{id}", h.deletePackage)
	r.Get("/api/software/deployments", h.listDeployments)
	r.Get("/api/software/deployments/{id}", h.getDeployment)
	r.Get("/api/software/deployments/{id}/tasks", h.listTasks)
	r.Post("/api/software/deployments", h.createDeployment)
	r.Get("/api/agent/packages/{id}/download", h.downloadPackage)

	return &packageFixture{handler: h, router: r, db: d, store: store, audit: auditor}
}

// do issues a request as the given operator. The user id has to be in the
// context because audit entries and created_by are read from it.
func (f *packageFixture) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "u-1"))
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// upload builds a real multipart body and posts it.
func (f *packageFixture) upload(t *testing.T, fields map[string]string, fileName, content string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	part, err := mw.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/software/packages", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "u-1"))
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func msiFields() map[string]string {
	return map[string]string{
		"name": "Contoso Widget", "version": "2.1.0",
		"os_target": OSTargetWindows, "package_type": PkgTypeMSI,
		"uninstall_args": "/x {ProductCode}",
	}
}

// seedPackage writes a package row and its file directly, for the tests that
// are about what happens to an existing package rather than about the upload.
func seedPackage(t *testing.T, f *packageFixture, id string, installArgs, uninstallArgs string) SoftwarePackage {
	t.Helper()
	storePath := filepath.Join(f.store, id+".msi")
	if err := os.WriteFile(storePath, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := SoftwarePackage{
		ID: id, Name: "Contoso Widget", Version: "2.1.0",
		OSTarget: OSTargetWindows, PackageType: PkgTypeMSI,
		FileName: "widget.msi", FileSize: 7,
		SHA256:      "0000000000000000000000000000000000000000000000000000000000000000",
		StoragePath: storePath, InstallArgs: installArgs, UninstallArgs: uninstallArgs,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := (&Repository{db: f.db}).CreatePackage(context.Background(), pkg); err != nil {
		t.Fatal(err)
	}
	return pkg
}

// TestUploadingAPackageStoresItWithItsHashAndKeepsTheFile is the happy path of
// the route that carries real software. The SHA-256 is the only thing that lets
// an endpoint detect that a download was corrupted, so it has to be computed
// from what was actually written rather than from what was declared.
func TestUploadingAPackageStoresItWithItsHashAndKeepsTheFile(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})

	rec := f.upload(t, msiFields(), "widget.msi", "MSI-CONTENT")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var pkg SoftwarePackage
	if err := json.Unmarshal(rec.Body.Bytes(), &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Name != "Contoso Widget" || pkg.Version != "2.1.0" {
		t.Errorf("package = %+v: name and version must round-trip", pkg)
	}
	if pkg.FileSize != int64(len("MSI-CONTENT")) {
		t.Errorf("file_size = %d, want %d", pkg.FileSize, len("MSI-CONTENT"))
	}
	// 64 hex characters is what sha256.Sum256 produces; anything else means the
	// column holds something other than the digest of the bytes.
	if len(pkg.SHA256) != 64 {
		t.Errorf("sha256 = %q (%d chars), want 64 hex characters", pkg.SHA256, len(pkg.SHA256))
	}
	if pkg.StoragePath != "" {
		t.Errorf("storage_path = %q, want empty: it is json:\"-\" and must not reach the console",
			pkg.StoragePath)
	}

	// The file has to be on disk, under a name that cannot collide with another
	// package's, and its contents have to be the uploaded bytes.
	onDisk, err := os.ReadFile(filepath.Join(f.store, pkg.ID+"_widget.msi"))
	if err != nil {
		t.Fatalf("the uploaded file is not on disk: %v", err)
	}
	if string(onDisk) != "MSI-CONTENT" {
		t.Errorf("stored bytes = %q, want %q", onDisk, "MSI-CONTENT")
	}
	if !f.audit.saw("software.upload") {
		t.Errorf("audit actions = %v, want a software.upload entry: the file pushed "+
			"to the fleet has to be attributable", f.audit.actions)
	}
}

// TestAnExeWithoutSilentSwitchesIsRefused: an .exe has no universal silent
// flag, so one uploaded without install_args opens a setup dialog on the
// endpoint's desktop and blocks on a human.
func TestAnExeWithoutSilentSwitchesIsRefused(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})

	fields := msiFields()
	fields["package_type"] = PkgTypeEXE
	delete(fields, "uninstall_args")

	rec := f.upload(t, fields, "setup.exe", "MZ...")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "install_args") {
		t.Errorf("the refusal does not name the missing field: %s", rec.Body.String())
	}
}

// TestAPackageTypeFromTheWrongPlatformIsRefused: the agent builds a different
// command per type, so an msi uploaded as a deb produces an unrunnable command
// on the endpoint.
func TestAPackageTypeFromTheWrongPlatformIsRefused(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})

	cases := []struct {
		name     string
		osTarget string
		pkgType  string
		fileName string
	}{
		{"a deb on windows", OSTargetWindows, "deb", "tool.deb"},
		{"an msi on linux", OSTargetLinux, PkgTypeMSI, "tool.msi"},
		{"a type no platform offers", OSTargetMacOS, "rpm", "tool.rpm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := msiFields()
			fields["os_target"] = tc.osTarget
			fields["package_type"] = tc.pkgType

			rec := f.upload(t, fields, tc.fileName, "content")
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400\nbody: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAMissingRequiredFieldNamesIt: the operator has four required fields, so
// the refusal has to say which one rather than only "required".
func TestAMissingRequiredFieldNamesIt(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})

	for _, drop := range []string{"name", "version", "os_target", "package_type"} {
		t.Run(drop, func(t *testing.T) {
			fields := msiFields()
			delete(fields, drop)

			rec := f.upload(t, fields, "widget.msi", "content")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\nbody: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			// With four of them required, naming one is enough to act on.
			if !strings.Contains(body, "required") {
				t.Errorf("refusal does not say the field is required: %s", body)
			}
		})
	}
}

// TestDeletingAPackageRemovesTheRowAndTheFile: the row is what the console
// reads; the file is what the agent would download. A row removed but a file
// left behind is disk that grows without limit and a package whose id still
// resolves to bytes on disk.
func TestDeletingAPackageRemovesTheRowAndTheFile(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})
	pkg := seedPackage(t, f, "pkg-delete-me", "/quiet", "/x {ProductCode}")

	rec := f.do(t, http.MethodDelete, "/api/software/packages/"+pkg.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var n int
	if err := f.db.Get(&n, `SELECT COUNT(*) FROM software_packages WHERE id = ?`, pkg.ID); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the package row is still there")
	}
	if _, err := os.Stat(pkg.StoragePath); !os.IsNotExist(err) {
		t.Errorf("the file at %s is still on disk", pkg.StoragePath)
	}
	if !f.audit.saw("software.delete") {
		t.Errorf("audit actions = %v, want a software.delete entry", f.audit.actions)
	}
}

// A missing package has to read as 404 rather than 500 on both the read and the
// delete: the console distinguishes "gone" from "broken" by the status.
func TestAMissingPackageIs404OnBothReadAndDelete(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		rec := f.do(t, method, "/api/software/packages/no-such-package", "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404\nbody: %s", method, rec.Code, rec.Body.String())
		}
	}
}

// TestThePackageListReadsAsAnArray: the console iterates this body, and Go
// encodes a nil slice as null.
func TestThePackageListReadsAsAnArray(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})

	rec := f.do(t, http.MethodGet, "/api/software/packages", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("empty list body = %s, want []", got)
	}
}

// TestTheAgentDownloadIsScopedToAnEnrolledAgent: downloadPackage serves the
// bytes an installer runs on the endpoint. Without the credential check any
// client that can reach the port can pull any package, and the package id is in
// the URL the agent is given.
func TestTheAgentDownloadIsScopedToAnEnrolledAgent(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})
	pkg := seedPackage(t, f, "pkg-download", "/quiet", "/x {ProductCode}")

	// pkgID and deviceID are separate arguments on purpose: the package id is
	// the URL segment, the device id is a header, and conflating them requests
	// a package that does not exist.
	get := func(pkgID, deviceID, secret string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/agent/packages/"+pkgID+"/download", nil)
		if deviceID != "" {
			req.Header.Set("X-Device-Id", deviceID)
		}
		if secret != "" {
			req.Header.Set("X-Device-Secret", secret)
		}
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		return rec
	}

	// The owning agent gets the bytes, and the headers an installer needs to
	// verify them.
	rec := get(pkg.ID, "dev-1", "agent-secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "payload" {
		t.Errorf("downloaded %q, want %q", got, "payload")
	}
	if got := rec.Header().Get("X-Package-SHA256"); got != pkg.SHA256 {
		t.Errorf("X-Package-SHA256 = %q, want %q: the agent verifies the digest "+
			"against this header", got, pkg.SHA256)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "widget.msi") {
		t.Errorf("Content-Disposition = %q, want it to name the package file", cd)
	}

	for _, tc := range []struct{ name, deviceID, secret string }{
		{"no credentials", "", ""},
		{"wrong secret", "dev-1", "not-the-secret"},
		{"a valid secret on a device that does not exist", "dev-9", "agent-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := get(pkg.ID, tc.deviceID, tc.secret); rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401\nbody: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAnUnenrolledAgentCannotDownloadByGuessingAPackageID: an unknown id and an
// unauthenticated caller must not be told apart in a way that reveals which
// package ids exist.
func TestAnUnenrolledAgentCannotDownloadByGuessingAPackageID(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})
	seedPackage(t, f, "pkg-real", "/quiet", "/x {ProductCode}")

	fetch := func(id string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/agent/packages/"+id+"/download", nil)
		req.Header.Set("X-Device-Id", "dev-1")
		req.Header.Set("X-Device-Secret", "agent-secret")
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		return rec.Code
	}

	unknown := fetch("no-such-package")
	if unknown != http.StatusNotFound {
		t.Errorf("unknown package answered %d, want 404", unknown)
	}
	if real := fetch("pkg-real"); real != http.StatusOK {
		t.Errorf("real package answered %d, want 200: the fixture download is broken, "+
			"not the handler", real)
	}
}

// createDeployment is the route that pushes software to a fleet, so each of its
// refusals is one less rollout that half-runs.
func TestCreateDeploymentRefusesAnIncompleteRequest(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")

	cases := []struct {
		name string
		body string
	}{
		{"not json", `name=`},
		{"no name", `{"package_id":"pkg-1","target_type":"device","target_id":"dev-1"}`},
		{"no package", `{"name":"rollout","target_type":"device","target_id":"dev-1"}`},
		{"no target type", `{"name":"rollout","package_id":"pkg-1","target_id":"dev-1"}`},
		{"an action that is neither verb", `{"name":"rollout","package_id":"pkg-1",` +
			`"target_type":"device","target_id":"dev-1","action":"reinstall"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(t, http.MethodPost, "/api/software/deployments", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400\nbody: %s", rec.Code, rec.Body.String())
			}
			var n int
			if err := f.db.Get(&n, `SELECT COUNT(*) FROM deployment_tasks`); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("task rows = %d, want 0", n)
			}
		})
	}
}

// TestADeploymentToAnAgentThatCannotRunItIsRefused: an agent that predates the
// command answers with an error that lands in agent_commands, not in
// deployment_tasks, so the task row never moves and nothing reaps it. The
// operator sees a rollout that is 'running' forever on a healthy endpoint.
func TestADeploymentToAnAgentThatCannotRunItIsRefused(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")

	// A device whose stored capability list does not name the command. NULL and
	// unparseable are both treated as lacking everything, so NULL is what a
	// device that has never reconnected with a capability-reporting build has.
	f.db.MustExec(`UPDATE devices SET capabilities = NULL WHERE id = 'dev-1'`)

	rec := f.do(t, http.MethodPost, "/api/software/deployments",
		`{"name":"rollout","package_id":"pkg-1","target_type":"device","target_id":"dev-1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a command the endpoint will drop was "+
			"dispatched anyway\nbody: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "dev-1") {
		t.Errorf("the refusal does not name the endpoint that cannot run it: %s", body)
	}

	var n int
	if err := f.db.Get(&n, `SELECT COUNT(*) FROM deployment_tasks`); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("task rows = %d, want 0: a refused deployment must leave nothing "+
			"behind to sit in 'dispatched' forever", n)
	}
}

// TestAnUninstallWithNoUninstallArgsIsRefused: an uninstaller with no switches
// opens an interactive window on the endpoint, which is the exact thing this
// feature exists to never do.
func TestAnUninstallWithNoUninstallArgsIsRefused(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})
	seedPackage(t, f, "pkg-1", "/quiet", "")

	rec := f.do(t, http.MethodPost, "/api/software/deployments",
		`{"name":"rollback","package_id":"pkg-1","target_type":"device","target_id":"dev-1",`+
			`"action":"uninstall"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "uninstall_args") {
		t.Errorf("the refusal does not name the field to fix: %s", rec.Body.String())
	}
}

// TestADeploymentToNothingIsRefused: an empty target set creates a deployment
// with zero tasks, which reads as a completed rollout that did nothing.
func TestADeploymentToNothingIsRefused(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")

	cases := []struct {
		name string
		body string
	}{
		{"a group with no members", `{"name":"rollout","package_id":"pkg-1",` +
			`"target_type":"group","target_id":"grp-empty"}`},
		{"every endpoint retired", `{"name":"rollout","package_id":"pkg-1",` +
			`"target_type":"device","target_id":"dev-old"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(t, http.MethodPost, "/api/software/deployments", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400\nbody: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestARetiredEndpointIsNeverTargeted: a retired device keeps its row so audit
// references resolve, and its secret is cleared. A rollout that reached it
// would push software to hardware that has left the fleet.
func TestARetiredEndpointIsNeverTargeted(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")

	rec := f.do(t, http.MethodPost, "/api/software/deployments",
		`{"name":"rollout","package_id":"pkg-1","target_type":"all"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var tasks []DeploymentTask
	if err := f.db.Select(&tasks, `SELECT device_id FROM deployment_tasks`); err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("task rows = %d, want 2 (dev-1 and dev-2 only)", len(tasks))
	}
	for _, task := range tasks {
		if task.DeviceID == "dev-old" {
			t.Error("a retired endpoint was given a deployment task")
		}
	}
}

// TestADeploymentDispatchesToOnlineAgentsAndMarksThoseTasksDispatched: the
// dispatch is the whole point of the route. A task created but never sent is a
// rollout that sits at 'pending' with nothing explaining it.
func TestADeploymentDispatchesToOnlineAgentsAndMarksThoseTasksDispatched(t *testing.T) {
	hub := &liveHub{online: map[string]bool{"dev-1": true, "dev-2": false}}
	f := newPackageFixture(t, hub)
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")

	rec := f.do(t, http.MethodPost, "/api/software/deployments",
		`{"name":"rollout","package_id":"pkg-1","target_type":"all"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Deployment     SoftwareDeployment `json:"deployment"`
		TasksTotal     int                `json:"tasks_total"`
		DispatchedLive int                `json:"dispatched_live"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TasksTotal != 2 {
		t.Errorf("tasks_total = %d, want 2", body.TasksTotal)
	}
	if body.DispatchedLive != 1 {
		t.Errorf("dispatched_live = %d, want 1: only dev-1 was online", body.DispatchedLive)
	}
	if body.Deployment.Status != "running" {
		t.Errorf("deployment status = %q, want \"running\"", body.Deployment.Status)
	}

	var dispatched, pending int
	if err := f.db.Get(&dispatched,
		`SELECT COUNT(*) FROM deployment_tasks WHERE status = ?`, TaskStatusDispatched); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Get(&pending,
		`SELECT COUNT(*) FROM deployment_tasks WHERE status = ?`, TaskStatusPending); err != nil {
		t.Fatal(err)
	}
	if dispatched != 1 || pending != 1 {
		t.Errorf("tasks: %d dispatched, %d pending; want 1 and 1", dispatched, pending)
	}
	if !f.audit.saw("software.install") {
		t.Errorf("audit actions = %v, want a software.install entry", f.audit.actions)
	}
}

// TestAnUninstallCarriesTheUninstallArgsAndNoInstallerPath: the uninstaller is
// already on the endpoint. Sending a download_url would be sending a file that
// should not exist.
func TestAnUninstallCarriesTheUninstallArgsAndNoInstallerPath(t *testing.T) {
	hub := &capturingDeploymentHub{online: true}
	f := newPackageFixture(t, hub)
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")

	rec := f.do(t, http.MethodPost, "/api/software/deployments",
		`{"name":"rollback","package_id":"pkg-1","target_type":"device","target_id":"dev-1",`+
			`"action":"uninstall"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var env struct {
		Command string `json:"command"`
		Payload struct {
			DownloadURL   *string `json:"download_url"`
			SHA256        *string `json:"sha256"`
			UninstallArgs string  `json:"uninstall_args"`
			PackageType   string  `json:"package_type"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(hub.payload, &env); err != nil {
		t.Fatal(err)
	}
	if env.Command != "software.uninstall" {
		t.Errorf("command = %q, want \"software.uninstall\"", env.Command)
	}
	if env.Payload.DownloadURL != nil {
		t.Errorf("download_url = %q, want absent on an uninstall", *env.Payload.DownloadURL)
	}
	if env.Payload.SHA256 != nil {
		t.Errorf("sha256 = %q, want absent on an uninstall", *env.Payload.SHA256)
	}
	if env.Payload.UninstallArgs != "/x {ProductCode}" {
		t.Errorf("uninstall_args = %q, want the package's own", env.Payload.UninstallArgs)
	}
}

// TestAnInstallCarriesTheDownloadURLAndDigest: the mirror of the case above, and
// the one that has to be right or the endpoint runs a truncated installer.
func TestAnInstallCarriesTheDownloadURLAndDigest(t *testing.T) {
	hub := &capturingDeploymentHub{online: true}
	f := newPackageFixture(t, hub)
	pkg := seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")

	rec := f.do(t, http.MethodPost, "/api/software/deployments",
		`{"name":"rollout","package_id":"pkg-1","target_type":"device","target_id":"dev-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var env struct {
		Command string `json:"command"`
		Payload struct {
			DownloadURL string `json:"download_url"`
			SHA256      string `json:"sha256"`
			FileName    string `json:"file_name"`
			InstallArgs string `json:"install_args"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(hub.payload, &env); err != nil {
		t.Fatal(err)
	}
	if env.Command != "software.install" {
		t.Errorf("command = %q, want \"software.install\"", env.Command)
	}
	want := "/api/agent/packages/" + pkg.ID + "/download"
	if env.Payload.DownloadURL != want {
		t.Errorf("download_url = %q, want %q", env.Payload.DownloadURL, want)
	}
	if env.Payload.SHA256 != pkg.SHA256 {
		t.Errorf("sha256 = %q, want %q: the endpoint verifies the download against "+
			"this", env.Payload.SHA256, pkg.SHA256)
	}
	if env.Payload.FileName != "widget.msi" {
		t.Errorf("file_name = %q, want \"widget.msi\"", env.Payload.FileName)
	}
	if env.Payload.InstallArgs != "/quiet" {
		t.Errorf("install_args = %q, want \"/quiet\"", env.Payload.InstallArgs)
	}
}

// TestAGroupTargetOnlyReachesItsOwnMembers: a rollout aimed at one group must
// not reach every device in the fleet.
func TestAGroupTargetOnlyReachesItsOwnMembers(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")
	f.db.MustExec(`INSERT INTO device_group_members (device_id, group_id) VALUES ('dev-old','grp-1')`)

	rec := f.do(t, http.MethodPost, "/api/software/deployments",
		`{"name":"rollout","package_id":"pkg-1","target_type":"group","target_id":"grp-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var devices []string
	if err := f.db.Select(&devices, `SELECT device_id FROM deployment_tasks ORDER BY device_id`); err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices[0] != "dev-1" || devices[1] != "dev-2" {
		t.Errorf("targeted %v, want [dev-1 dev-2]", devices)
	}
}

// TestTheDeploymentAndTaskReadsAnswerWithArrays: the console's deployment list
// and task drill-down both iterate these bodies without a null check.
func TestTheDeploymentAndTaskReadsAnswerWithArrays(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")

	if rec := f.do(t, http.MethodGet, "/api/software/deployments", ""); rec.Code != http.StatusOK ||
		strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty deployment list = %d %q, want 200 []", rec.Code, rec.Body.String())
	}
	if rec := f.do(t, http.MethodGet, "/api/software/deployments/no-such/tasks", ""); rec.Code != http.StatusOK ||
		strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty task list = %d %q, want 200 []", rec.Code, rec.Body.String())
	}
	if rec := f.do(t, http.MethodGet, "/api/software/deployments/no-such", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown deployment = %d, want 404", rec.Code)
	}
}

// TestTheDeploymentListRollsUpItsTasks: the counters are what the console shows
// on the list, so they have to be computed rather than left at zero.
func TestTheDeploymentListRollsUpItsTasks(t *testing.T) {
	f := newPackageFixture(t, &liveHub{})
	seedPackage(t, f, "pkg-1", "/quiet", "/x {ProductCode}")

	rec := f.do(t, http.MethodPost, "/api/software/deployments",
		`{"name":"rollout","package_id":"pkg-1","target_type":"all"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Deployment SoftwareDeployment `json:"deployment"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	f.db.MustExec(`UPDATE deployment_tasks SET status = ? WHERE device_id = 'dev-1'`,
		TaskStatusSuccess)
	f.db.MustExec(`UPDATE deployment_tasks SET status = ? WHERE device_id = 'dev-2'`,
		TaskStatusFailed)

	rec = f.do(t, http.MethodGet, "/api/software/deployments", "")
	var deps []SoftwareDeployment
	if err := json.Unmarshal(rec.Body.Bytes(), &deps); err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 {
		t.Fatalf("deployments = %d, want 1", len(deps))
	}
	got := deps[0]
	if got.ID != created.Deployment.ID {
		t.Errorf("id = %q, want %q", got.ID, created.Deployment.ID)
	}
	if got.TotalTasks != 2 || got.SuccessTasks != 1 || got.FailedTasks != 1 {
		t.Errorf("rollup = %d total, %d success, %d failed; want 2, 1, 1",
			got.TotalTasks, got.SuccessTasks, got.FailedTasks)
	}
	if got.PackageName != "Contoso Widget" {
		t.Errorf("package_name = %q, want it joined from the package", got.PackageName)
	}
}

// capturingDeploymentHub keeps the last command so a test can assert on the
// wire format of a dispatch.
type capturingDeploymentHub struct {
	online  bool
	payload []byte
}

func (h *capturingDeploymentHub) Online(string) bool { return h.online }

func (h *capturingDeploymentHub) SendTo(_ string, msg []byte) bool {
	h.payload = msg
	return true
}

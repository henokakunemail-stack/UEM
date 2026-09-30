package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
	softwaredeployment "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/software-deployment"
	"github.com/jmoiron/sqlx"
)

// Device identities used by the software-deployment E2E suite. The agent-facing
// routes authenticate on the X-Device-Id / X-Device-Secret pair, so these
// secrets are hashed into the seeded devices below and replayed verbatim by
// the requests that stand in for an agent.
const (
	devWin1ID     = "dev-win-1"
	devWin1Secret = "win1-device-secret"
	devWin2ID     = "dev-win-2"
	devWin2Secret = "win2-device-secret"
	devLin1ID     = "dev-lin-1"
	devLin1Secret = "lin1-device-secret"

	// testCaps is the capability list a current agent advertises on its hello
	// frame. The deployment handler gates on it, so a seeded device without it
	// is a device that has never connected -- a state worth testing separately
	// rather than as the default for the happy path.
	testCaps = `["ping","inventory.collect","software.install","software.uninstall","exec.run","term.open","patch.scan","patch.install"]`
)

type mockHub struct {
	onlineDevices map[string]bool
	sentMessages  map[string][][]byte
}

func newMockHub() *mockHub {
	return &mockHub{
		onlineDevices: make(map[string]bool),
		sentMessages:  make(map[string][][]byte),
	}
}

func (m *mockHub) Online(deviceID string) bool {
	return m.onlineDevices[deviceID]
}

func (m *mockHub) SendTo(deviceID string, payload []byte) bool {
	m.sentMessages[deviceID] = append(m.sentMessages[deviceID], payload)
	return true
}

func newSoftwareEnv(t *testing.T) (*httptest.Server, *sqlx.DB, *auth.JWTService, *mockHub, string) {
	t.Helper()
	tempDir := t.TempDir()
	d, err := db.Open(filepath.Join(tempDir, "software-e2e.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	jwtSvc := auth.NewJWTService("soft-e2e-secret-0123456789abcdef", time.Minute, time.Hour)
	hub := newMockHub()
	softRepo := softwaredeployment.NewRepository(d)
	deviceRepo := devicemgmt.NewRepository(d)
	storageDir := filepath.Join(tempDir, "packages")
	softH := softwaredeployment.NewHandler(softRepo, hub, nil, storageDir, jwtSvc.RequireAuth, deviceRepo)

	r := chi.NewRouter()
	loginH := auth.NewLoginHandler(d, jwtSvc)
	loginH.Register(r)
	softH.Register(r)

	srv := httptest.NewServer(r)
	t.Cleanup(func() { srv.Close() })
	return srv, d, jwtSvc, hub, storageDir
}

func TestE2ESoftwareDeployment(t *testing.T) {
	srv, d, jwtSvc, hub, _ := newSoftwareEnv(t)
	ctx := context.Background()

	// 1. Seed users: admin, technician, viewer
	now := time.Now().UTC()
	hashPass, _ := auth.HashPassword("secret123")
	_, err := d.ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, role, created_at, updated_at)
		VALUES
		('u-admin', 'admin', ?, 'admin', ?, ?),
		('u-tech', 'tech', ?, 'technician', ?, ?),
		('u-viewer', 'viewer', ?, 'viewer', ?, ?)`,
		hashPass, now, now,
		hashPass, now, now,
		hashPass, now, now)
	if err != nil {
		t.Fatalf("seed users: %v", err)
	}

	adminTokens, _ := jwtSvc.Issue("u-admin", "admin", rbac.RoleAdmin)
	techTokens, _ := jwtSvc.Issue("u-tech", "tech", rbac.RoleTechnician)
	viewerTokens, _ := jwtSvc.Issue("u-viewer", "viewer", rbac.RoleViewer)

	// 2. Seed devices: 2 Windows, 1 Linux
	// The agent-facing endpoints authenticate on the device secret, so the
	// hashes must be real SHA-256 digests of the secrets used later below.
	//
	// capabilities is what the agent writes on its hello. The deployment handler
	// refuses to create a deployment for an endpoint whose agent never advertised
	// the command, because a command an agent does not recognise produces a task
	// that never completes and never explains itself. Seeding it here is what a
	// real agent does on connect, so omitting it would test a state no agent is
	// ever in.
	_, err = d.ExecContext(ctx, `
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status, last_seen_at, enrolled_at, device_secret_hash, site, capabilities, created_at, updated_at)
		VALUES
		('dev-win-1', 'DESKTOP-WIN1', 'windows', '10.0', '1.0.0', 'online', ?, ?, ?, 'hq', ?, ?, ?),
		('dev-win-2', 'DESKTOP-WIN2', 'windows', '10.0', '1.0.0', 'offline', ?, ?, ?, 'hq', ?, ?, ?),
		('dev-lin-1', 'SRV-LIN1', 'linux', '12.0', '1.0.0', 'online', ?, ?, ?, 'branch', ?, ?, ?)`,
		now, now, devicemgmt.HashToken(devWin1Secret), testCaps, now, now,
		now, now, devicemgmt.HashToken(devWin2Secret), testCaps, now, now,
		now, now, devicemgmt.HashToken(devLin1Secret), testCaps, now, now)
	if err != nil {
		t.Fatalf("seed devices: %v", err)
	}

	// Mark dev-win-1 as online in hub
	hub.onlineDevices["dev-win-1"] = true

	// 3. Test Package Upload (Technician role)
	fakeMSIContent := []byte("MSI_BINARY_PAYLOAD_TEST_DATA_V1")
	hasher := sha256.New()
	hasher.Write(fakeMSIContent)
	expectedSHA256 := hex.EncodeToString(hasher.Sum(nil))

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("name", "7-Zip Archiver")
	_ = writer.WriteField("version", "24.08")
	_ = writer.WriteField("os_target", "windows")
	_ = writer.WriteField("package_type", "msi")
	_ = writer.WriteField("install_args", "/qn /norestart")
	part, _ := writer.CreateFormFile("file", "7zip.msi")
	_, _ = io.Copy(part, bytes.NewReader(fakeMSIContent))
	_ = writer.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/software/packages", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+techTokens.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload package: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
	}

	var pkg softwaredeployment.SoftwarePackage
	if err := json.NewDecoder(resp.Body).Decode(&pkg); err != nil {
		t.Fatalf("decode package: %v", err)
	}

	if pkg.Name != "7-Zip Archiver" || pkg.SHA256 != expectedSHA256 || pkg.FileSize != int64(len(fakeMSIContent)) {
		t.Fatalf("mismatched package data: %+v", pkg)
	}

	// 4. Test RBAC: Viewer cannot upload
	reqViewer, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/software/packages", bytes.NewReader([]byte("{}")))
	reqViewer.Header.Set("Authorization", "Bearer "+viewerTokens.AccessToken)
	respViewer, err := http.DefaultClient.Do(reqViewer)
	if err != nil {
		t.Fatalf("viewer upload: %v", err)
	}
	respViewer.Body.Close()
	if respViewer.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for viewer upload, got %d", respViewer.StatusCode)
	}

	// 5. Test Download Package Binary (agent endpoint: requires device credentials)
	dlReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/agent/packages/"+pkg.ID+"/download", nil)
	dlReq.Header.Set("X-Device-Id", devWin1ID)
	dlReq.Header.Set("X-Device-Secret", devWin1Secret)
	dlResp, err := http.DefaultClient.Do(dlReq)
	if err != nil {
		t.Fatalf("download package: %v", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(dlResp.Body)
		t.Fatalf("expected 200 OK download, got %d: %s", dlResp.StatusCode, body)
	}
	downloadedBytes, _ := io.ReadAll(dlResp.Body)
	if !bytes.Equal(downloadedBytes, fakeMSIContent) {
		t.Fatalf("downloaded content mismatch")
	}

	// 6. Test Create Deployment (Target All Windows Devices)
	depReqBody := map[string]string{
		"name":        "Rollout 7-Zip",
		"package_id":  pkg.ID,
		"target_type": "all",
	}
	depJSON, _ := json.Marshal(depReqBody)
	depReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/software/deployments", bytes.NewReader(depJSON))
	depReq.Header.Set("Content-Type", "application/json")
	depReq.Header.Set("Authorization", "Bearer "+techTokens.AccessToken)

	depResp, err := http.DefaultClient.Do(depReq)
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	defer depResp.Body.Close()

	if depResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created deployment, got %d", depResp.StatusCode)
	}

	var depResult struct {
		Deployment     softwaredeployment.SoftwareDeployment `json:"deployment"`
		TasksTotal     int                                   `json:"tasks_total"`
		DispatchedLive int                                   `json:"dispatched_live"`
	}
	if err := json.NewDecoder(depResp.Body).Decode(&depResult); err != nil {
		t.Fatalf("decode deployment result: %v", err)
	}

	// Should resolve 2 Windows devices (dev-win-1 and dev-win-2), ignoring Linux
	if depResult.TasksTotal != 2 {
		t.Fatalf("expected 2 tasks for windows target, got %d", depResult.TasksTotal)
	}
	// dev-win-1 was online in hub, so dispatched_live should be 1
	if depResult.DispatchedLive != 1 {
		t.Fatalf("expected 1 live dispatched task, got %d", depResult.DispatchedLive)
	}

	// Verify command was sent to dev-win-1 via hub
	if len(hub.sentMessages["dev-win-1"]) == 0 {
		t.Fatalf("hub did not dispatch command to dev-win-1")
	}

	// 7. Test List Tasks & Agent Progress Reporting
	tasksReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/software/deployments/"+depResult.Deployment.ID+"/tasks", nil)
	tasksReq.Header.Set("Authorization", "Bearer "+viewerTokens.AccessToken)
	tasksResp, err := http.DefaultClient.Do(tasksReq)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	defer tasksResp.Body.Close()

	var tasks []softwaredeployment.DeploymentTask
	if err := json.NewDecoder(tasksResp.Body).Decode(&tasks); err != nil {
		t.Fatalf("decode tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}

	// Find dev-win-1 task and report progress
	var task1, task2 softwaredeployment.DeploymentTask
	for _, tsk := range tasks {
		if tsk.DeviceID == "dev-win-1" {
			task1 = tsk
		} else if tsk.DeviceID == "dev-win-2" {
			task2 = tsk
		}
	}

	// Report task1 downloading -> installing -> success
	prog1 := softwaredeployment.TaskProgressReport{
		TaskID: task1.ID,
		Status: softwaredeployment.TaskStatusDownloading,
	}
	p1Bytes, _ := json.Marshal(prog1)
	p1Req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/agent/tasks/"+task1.ID+"/progress", bytes.NewReader(p1Bytes))
	p1Req.Header.Set("Content-Type", "application/json")
	p1Req.Header.Set("X-Device-Id", devWin1ID)
	p1Req.Header.Set("X-Device-Secret", devWin1Secret)
	p1Resp, _ := http.DefaultClient.Do(p1Req)
	p1Resp.Body.Close()

	exitCode := 0
	logSuccess := "Installation completed successfully."
	progSuccess := softwaredeployment.TaskProgressReport{
		TaskID:    task1.ID,
		Status:    softwaredeployment.TaskStatusSuccess,
		ExitCode:  &exitCode,
		OutputLog: &logSuccess,
	}
	psBytes, _ := json.Marshal(progSuccess)
	psReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/agent/tasks/"+task1.ID+"/progress", bytes.NewReader(psBytes))
	psReq.Header.Set("Content-Type", "application/json")
	psReq.Header.Set("X-Device-Id", devWin1ID)
	psReq.Header.Set("X-Device-Secret", devWin1Secret)
	psResp, _ := http.DefaultClient.Do(psReq)
	psResp.Body.Close()

	// Report task2 failed
	failCode := 1603
	failLog := "Fatal error during installation."
	errMsg := "Action failed"
	progFail := softwaredeployment.TaskProgressReport{
		TaskID:       task2.ID,
		Status:       softwaredeployment.TaskStatusFailed,
		ExitCode:     &failCode,
		OutputLog:    &failLog,
		ErrorMessage: &errMsg,
	}
	pfBytes, _ := json.Marshal(progFail)
	pfReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/agent/tasks/"+task2.ID+"/progress", bytes.NewReader(pfBytes))
	pfReq.Header.Set("Content-Type", "application/json")
	pfReq.Header.Set("X-Device-Id", devWin2ID)
	pfReq.Header.Set("X-Device-Secret", devWin2Secret)
	pfResp, _ := http.DefaultClient.Do(pfReq)
	pfResp.Body.Close()

	// 8. Verify Deployment Status Auto-Completion & Aggregations
	depCheckReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/software/deployments/"+depResult.Deployment.ID, nil)
	depCheckReq.Header.Set("Authorization", "Bearer "+viewerTokens.AccessToken)
	depCheckResp, err := http.DefaultClient.Do(depCheckReq)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	defer depCheckResp.Body.Close()

	var finalDep softwaredeployment.SoftwareDeployment
	if err := json.NewDecoder(depCheckResp.Body).Decode(&finalDep); err != nil {
		t.Fatalf("decode final deployment: %v", err)
	}

	if finalDep.Status != "completed" {
		t.Fatalf("expected deployment status 'completed', got %q", finalDep.Status)
	}
	if finalDep.TotalTasks != 2 || finalDep.SuccessTasks != 1 || finalDep.FailedTasks != 1 {
		t.Fatalf("expected 2 total, 1 success, 1 failed; got total=%d, success=%d, failed=%d",
			finalDep.TotalTasks, finalDep.SuccessTasks, finalDep.FailedTasks)
	}
	if finalDep.CompletedAt == nil {
		t.Fatalf("expected completed_at timestamp to be set")
	}

	// 9. Test Delete Package (Admin only)
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/software/packages/"+pkg.ID, nil)
	delReq.Header.Set("Authorization", "Bearer "+adminTokens.AccessToken)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete package: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for admin delete, got %d", delResp.StatusCode)
	}

	// Verify package is deleted from DB
	getDeletedReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/software/packages/"+pkg.ID, nil)
	getDeletedReq.Header.Set("Authorization", "Bearer "+viewerTokens.AccessToken)
	getDeletedResp, _ := http.DefaultClient.Do(getDeletedReq)
	getDeletedResp.Body.Close()
	if getDeletedResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found for deleted package, got %d", getDeletedResp.StatusCode)
	}
}

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
	softwaredeployment "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/software-deployment"
)

// readBody returns a response body and closes it, so an assertion can include
// the server's own explanation in its failure message.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}

// uninstallEnv seeds the minimum an uninstall deployment needs: a technician,
// one Windows endpoint whose agent advertises the command, and a package that
// carries uninstall_args.
type uninstallEnv struct {
	server   *httptest.Server
	db       *sqlx.DB
	hub      *mockHub
	token    string
	pkg      softwaredeployment.SoftwarePackage
	deviceID string
	secret   string
}

func setupUninstall(t *testing.T, capabilities string) *uninstallEnv {
	t.Helper()
	srv, d, jwtSvc, hub, storageDir := newSoftwareEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()

	hashPass, _ := auth.HashPassword("secret123")
	_, err := d.ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, role, created_at, updated_at)
		VALUES ('u-tech', 'tech', ?, 'technician', ?, ?)`,
		hashPass, now, now)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	const (
		deviceID = "dev-win-1"
		secret   = "win1-device-secret"
	)
	_, err = d.ExecContext(ctx, `
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
			last_seen_at, enrolled_at, device_secret_hash, site, capabilities, created_at, updated_at)
		VALUES (?, 'DESKTOP-WIN1', 'windows', '10.0', '1.0.0', 'online', ?, ?, ?, 'hq', ?, ?, ?)`,
		deviceID, now, now, devicemgmt.HashToken(secret), capabilities, now, now)
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
	hub.onlineDevices[deviceID] = true

	// The package is an .exe, so install_args is mandatory; uninstall_args is
	// what this whole path turns on.
	pkg := softwaredeployment.SoftwarePackage{
		ID:            "pkg-winrar",
		Name:          "WinRAR",
		Version:       "7.23",
		OSTarget:      softwaredeployment.OSTargetWindows,
		PackageType:   softwaredeployment.PkgTypeEXE,
		FileName:      "winrar.exe",
		FileSize:      1024,
		SHA256:        "deadbeef",
		StoragePath:   filepath.Join(storageDir, "winrar.exe"),
		InstallArgs:   "/s",
		UninstallArgs: "/s",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO software_packages (id, name, version, os_target, package_type, file_name,
			file_size, sha256, storage_path, install_args, uninstall_args, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pkg.ID, pkg.Name, pkg.Version, pkg.OSTarget, pkg.PackageType, pkg.FileName,
		pkg.FileSize, pkg.SHA256, pkg.StoragePath, pkg.InstallArgs, pkg.UninstallArgs,
		now, now); err != nil {
		t.Fatalf("seed package: %v", err)
	}

	tokens, err := jwtSvc.Issue("u-tech", "tech", "technician")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	return &uninstallEnv{server: srv, db: d, hub: hub, token: tokens.AccessToken, pkg: pkg, deviceID: deviceID, secret: secret}
}

func (e *uninstallEnv) createDeployment(t *testing.T, body map[string]any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, e.server.URL+"/api/software/deployments", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	return resp
}

// An uninstall is a different command from an install and has to reach the
// agent as one. This checks the shape the agent actually parses, because the
// failure it prevents is silent: an agent handed a download_url for a package it
// does not have would try to fetch a file that was never uploaded.
func TestE2EUninstallDispatchShape(t *testing.T) {
	env := setupUninstall(t, testCaps)

	resp := env.createDeployment(t, map[string]any{
		"name":        "Remove WinRAR",
		"package_id":  env.pkg.ID,
		"target_type": "device",
		"target_id":   env.deviceID,
		"action":      "uninstall",
	})
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}

	sent := env.hub.sentMessages[env.deviceID]
	if len(sent) == 0 {
		t.Fatal("no command dispatched to the online endpoint")
	}
	var envelope struct {
		Type    string         `json:"type"`
		Command string         `json:"command"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(sent[len(sent)-1], &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.Command != "software.uninstall" {
		t.Errorf("command = %q, want software.uninstall", envelope.Command)
	}
	if _, ok := envelope.Payload["download_url"]; ok {
		t.Error("an uninstall payload must not carry download_url")
	}
	if _, ok := envelope.Payload["sha256"]; ok {
		t.Error("an uninstall payload must not carry sha256")
	}
	if got := envelope.Payload["uninstall_args"]; got != "/s" {
		t.Errorf("uninstall_args = %v, want /s", got)
	}
	if got := envelope.Payload["package_name"]; got != "WinRAR" {
		t.Errorf("package_name = %v, want WinRAR", got)
	}
}

// A package with no uninstall_args would run an uninstaller with no silent
// switches, which opens a window on the endpoint's desktop. That has to be
// refused at the boundary, before any task row exists.
func TestE2EUninstallRequiresSilentArgs(t *testing.T) {
	env := setupUninstall(t, testCaps)
	_, err := env.db.Exec(`UPDATE software_packages SET uninstall_args = '' WHERE id = ?`, env.pkg.ID)
	if err != nil {
		t.Fatal(err)
	}

	resp := env.createDeployment(t, map[string]any{
		"name":        "Remove WinRAR",
		"package_id":  env.pkg.ID,
		"target_type": "device",
		"target_id":   env.deviceID,
		"action":      "uninstall",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "uninstall_args") {
		t.Errorf("error should name the missing field, got %s", body)
	}

	// No deployment may exist: the refusal has to happen before the row.
	var n int
	if err := env.db.Get(&n, `SELECT COUNT(*) FROM software_deployments`); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected no deployment row to be created, got %d", n)
	}
}

// An agent that predates the command answers with an error that lands in
// agent_commands, not deployment_tasks, so the task row never moves and the
// orphan sweep does not reap it either. The deployment must be refused instead.
func TestE2EUninstallRefusesEndpointWithoutCapability(t *testing.T) {
	env := setupUninstall(t, `["ping","inventory.collect","software.install","exec.run"]`)

	resp := env.createDeployment(t, map[string]any{
		"name":        "Remove WinRAR",
		"package_id":  env.pkg.ID,
		"target_type": "device",
		"target_id":   env.deviceID,
		"action":      "uninstall",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if body := readBody(t, resp); !strings.Contains(body, "software.uninstall") {
		t.Errorf("error should name the unsupported command, got %s", body)
	}
}

// An endpoint that has never sent a hello has no capability list at all, and
// that is not permission. It is the state of every device enrolled before the
// agent reported capabilities, so it is the most likely one in the field.
func TestE2EDeploymentRefusesEndpointThatNeverSaidHello(t *testing.T) {
	env := setupUninstall(t, "")

	resp := env.createDeployment(t, map[string]any{
		"name":        "Install WinRAR",
		"package_id":  env.pkg.ID,
		"target_type": "device",
		"target_id":   env.deviceID,
		"action":      "install",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a device with no capabilities, got %d: %s",
			resp.StatusCode, readBody(t, resp))
	}
}

// An empty action is an install, because that is what every caller sent before
// uninstall existed. A typo must not become an install though.
func TestE2EDeploymentActionDefaultsToInstallAndRejectsTypos(t *testing.T) {
	env := setupUninstall(t, testCaps)

	resp := env.createDeployment(t, map[string]any{
		"name":        "Deploy WinRAR",
		"package_id":  env.pkg.ID,
		"target_type": "device",
		"target_id":   env.deviceID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("an absent action should default to install, got %d: %s",
			resp.StatusCode, readBody(t, resp))
	}
	var result struct {
		Deployment softwaredeployment.SoftwareDeployment `json:"deployment"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Deployment.Action != "install" {
		t.Errorf("action = %q, want install", result.Deployment.Action)
	}

	// The list API has to report it back, or the console cannot tell an install
	// from an uninstall after the fact.
	listReq, _ := http.NewRequest(http.MethodGet, env.server.URL+"/api/software/deployments", nil)
	listReq.Header.Set("Authorization", "Bearer "+env.token)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	var deps []softwaredeployment.SoftwareDeployment
	if err := json.NewDecoder(listResp.Body).Decode(&deps); err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || deps[0].Action != "install" {
		t.Errorf("list returned %+v, want one install deployment", deps)
	}

	typo := env.createDeployment(t, map[string]any{
		"name":        "Remove WinRAR",
		"package_id":  env.pkg.ID,
		"target_type": "device",
		"target_id":   env.deviceID,
		"action":      "uninstal",
	})
	if typo.StatusCode != http.StatusBadRequest {
		t.Errorf("a misspelled action must be rejected, got %d", typo.StatusCode)
	}
}

// The task list is what an operator reads while a rollout runs, so it has to
// carry the argument the agent was told to run and the action the deployment
// performed. Both come from columns the API previously did not select at all.
func TestE2EDeploymentTaskCarriesArgsAndAction(t *testing.T) {
	env := setupUninstall(t, testCaps)

	resp := env.createDeployment(t, map[string]any{
		"name":        "Remove WinRAR",
		"package_id":  env.pkg.ID,
		"target_type": "device",
		"target_id":   env.deviceID,
		"action":      "uninstall",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, readBody(t, resp))
	}
	var created struct {
		Deployment softwaredeployment.SoftwareDeployment `json:"deployment"`
	}
	json.NewDecoder(resp.Body).Decode(&created)

	taskReq, _ := http.NewRequest(http.MethodGet,
		env.server.URL+"/api/software/deployments/"+created.Deployment.ID+"/tasks", nil)
	taskReq.Header.Set("Authorization", "Bearer "+env.token)
	taskResp, err := http.DefaultClient.Do(taskReq)
	if err != nil {
		t.Fatal(err)
	}
	defer taskResp.Body.Close()
	var tasks []softwaredeployment.DeploymentTask
	if err := json.NewDecoder(taskResp.Body).Decode(&tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Args != "/s" {
		t.Errorf("task args = %q, want /s", tasks[0].Args)
	}
}

// A deployment whose every agent dropped offline is not a completed rollout, and
// for an uninstall that is the more dangerous of the two: the console would show
// the machines as compliant when nothing is known about whether the removal
// happened.
func TestE2EUninstallAllAgentsLostIsNotReportedCompleted(t *testing.T) {
	env := setupUninstall(t, testCaps)

	resp := env.createDeployment(t, map[string]any{
		"name":        "Remove WinRAR",
		"package_id":  env.pkg.ID,
		"target_type": "device",
		"target_id":   env.deviceID,
		"action":      "uninstall",
	})
	var created struct {
		Deployment softwaredeployment.SoftwareDeployment `json:"deployment"`
	}
	json.NewDecoder(resp.Body).Decode(&created)

	// The agent dies mid-uninstall: the device goes offline and the task is left
	// running, which is what the sweep exists to close out.
	var taskID string
	if err := env.db.Get(&taskID,
		`SELECT id FROM deployment_tasks WHERE deployment_id = ?`, created.Deployment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`UPDATE devices SET status = 'offline' WHERE id = ?`, env.deviceID); err != nil {
		t.Fatal(err)
	}

	repo := softwaredeployment.NewRepository(env.db)
	n, err := repo.AbandonOrphanedTasks(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("sweep reaped %d tasks, want 1", n)
	}

	var status string
	if err := env.db.Get(&status,
		`SELECT status FROM software_deployments WHERE id = ?`, created.Deployment.ID); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Errorf("deployment status = %q, want failed -- a rollout where nothing is known is not a completed removal", status)
	}
}

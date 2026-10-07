package integration

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/transport"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
	remoteexec "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/remote-exec"
	"github.com/jmoiron/sqlx"
)

type mockExecHub struct {
	onlineDevices map[string]bool
	sentMessages  map[string][][]byte
}

func newMockExecHub() *mockExecHub {
	return &mockExecHub{
		onlineDevices: make(map[string]bool),
		sentMessages:  make(map[string][][]byte),
	}
}

func (m *mockExecHub) Online(deviceID string) bool {
	return m.onlineDevices[deviceID]
}

func (m *mockExecHub) SendTo(deviceID string, payload []byte) bool {
	m.sentMessages[deviceID] = append(m.sentMessages[deviceID], payload)
	return true
}

type mockExecAudit struct {
	logs []map[string]string
}

func (a *mockExecAudit) Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error {
	entry := map[string]string{
		"actor_type": actorType,
		"actor_id":   actorID,
		"action":     action,
		"target_id":  targetID,
	}
	for k, v := range details {
		entry[k] = v
	}
	a.logs = append(a.logs, entry)
	return nil
}

func newRemoteExecEnv(t *testing.T) (*httptest.Server, *sqlx.DB, *auth.JWTService, *mockExecHub, *remoteexec.Repository, *devicemgmt.Repository) {
	t.Helper()
	tempDir := t.TempDir()
	d, err := db.Open(filepath.Join(tempDir, "remote-exec-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	jwtSvc := auth.NewJWTService("remote-exec-secret-0123456789abcd", time.Minute, time.Hour)
	hub := newMockExecHub()
	deviceRepo := devicemgmt.NewRepository(d)
	execRepo := remoteexec.NewRepository(d)
	relay := remoteexec.NewTerminalRelay()
	auditMock := &mockExecAudit{}

	// Convert hub to *transport.Hub if needed, or wrap. Here we provide the handler
	// which accepts transport.Hub, so we can initialize a real Hub
	realHub := transport.NewHub()
	h := remoteexec.NewHandler(execRepo, realHub, relay, auditMock, jwtSvc.RequireAuth, deviceRepo, nil)

	r := chi.NewRouter()
	h.Register(r)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	return srv, d, jwtSvc, hub, execRepo, deviceRepo
}

func createTestDevice(t *testing.T, d *sqlx.DB, id, secret, hostname, osName string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := d.Exec(`
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, site, device_secret_hash, status, enrolled_at, created_at, updated_at)
		VALUES (?, ?, ?, '10.0', '1.0.0', 'HQ', ?, 'online', ?, ?, ?)`,
		id, hostname, osName, devicemgmt.HashToken(secret), now, now, now, now,
	)
	if err != nil {
		t.Fatalf("insert test device: %v", err)
	}
}

func TestRemoteExecution_RBAC(t *testing.T) {
	srv, d, jwtSvc, _, _, _ := newRemoteExecEnv(t)
	createTestDevice(t, d, "dev-rbac-1", "secret123", "RBAC-HOST", "windows")

	techToken, _ := jwtSvc.Issue("tech-user-1", "tech", rbac.RoleTechnician)
	adminToken, _ := jwtSvc.Issue("admin-user-1", "admin", rbac.RoleAdmin)
	viewerToken, _ := jwtSvc.Issue("viewer-user-1", "viewer", rbac.RoleViewer)

	reqBody := `{"shell": "powershell", "command": "Get-Date"}`

	// 1. Viewer role -> HTTP 403 Forbidden
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/devices/dev-rbac-1/exec", bytes.NewBufferString(reqBody))
	req.Header.Set("Authorization", "Bearer "+viewerToken.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("exec request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer expected 403 Forbidden, got: %d", resp.StatusCode)
	}

	// 2. Technician role with offline device -> 409 Conflict ("device is offline")
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/devices/dev-rbac-1/exec", bytes.NewBufferString(reqBody))
	req.Header.Set("Authorization", "Bearer "+techToken.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("exec request: %v", err)
	}
	defer resp.Body.Close()

	// Device is not in live hub so expected 409 Conflict, but NOT 403
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("technician expected 409 Conflict (offline), got: %d", resp.StatusCode)
	}

	// 3. Admin role with offline device -> 409 Conflict, NOT 403
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/devices/dev-rbac-1/exec", bytes.NewBufferString(reqBody))
	req.Header.Set("Authorization", "Bearer "+adminToken.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("exec request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("admin expected 409 Conflict (offline), got: %d", resp.StatusCode)
	}
}

func TestRemoteExecution_RepositoryOperations(t *testing.T) {
	_, d, _, _, repo, _ := newRemoteExecEnv(t)
	createTestDevice(t, d, "dev-repo-1", "secret123", "REPO-HOST", "windows")

	// Create user
	now := time.Now().UTC()
	_, err := d.Exec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"user-exec-1", "operator", "hash", rbac.RoleTechnician, now, now)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	execID := remoteexec.NewID()
	exec := &remoteexec.RemoteExecution{
		ID:          execID,
		DeviceID:    "dev-repo-1",
		OperatorID:  "user-exec-1",
		ShellType:   "powershell",
		CommandText: "Write-Output 'Hello E2E'",
		Status:      remoteexec.ExecStatusRunning,
		StartedAt:   now,
	}

	if err := repo.CreateExecution(context.Background(), exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	// Verify GetExecution
	got, err := repo.GetExecution(context.Background(), execID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if got.ID != execID || got.Status != remoteexec.ExecStatusRunning {
		t.Errorf("unexpected execution data: %+v", got)
	}
	if got.OperatorName != "operator" || got.Hostname != "REPO-HOST" {
		t.Errorf("enriched fields mismatch: operator=%s, host=%s", got.OperatorName, got.Hostname)
	}

	// Update Execution Result. The device id is the execution's own, because
	// the update is scoped to it: reporting a result for an execution this
	// device does not own now matches no rows.
	exitCode := 0
	output := "Hello E2E\n"
	err = repo.UpdateExecutionResult(context.Background(), exec.DeviceID, remoteexec.ExecResultReport{
		ExecutionID: execID,
		Status:      remoteexec.ExecStatusCompleted,
		ExitCode:    &exitCode,
		Output:      &output,
	})
	if err != nil {
		t.Fatalf("update execution result: %v", err)
	}

	// The scoping is a claim about ownership, so assert it rather than assuming
	// it: another device's report for this execution must be refused.
	if err := repo.UpdateExecutionResult(context.Background(), "some-other-device", remoteexec.ExecResultReport{
		ExecutionID: execID,
		Status:      remoteexec.ExecStatusFailed,
	}); !errors.Is(err, remoteexec.ErrNotFound) {
		t.Errorf("another device's report was accepted: err = %v, want ErrNotFound", err)
	}

	// Verify completion
	completed, err := repo.GetExecution(context.Background(), execID)
	if err != nil {
		t.Fatalf("get completed execution: %v", err)
	}
	if completed.Status != remoteexec.ExecStatusCompleted || *completed.ExitCode != 0 || *completed.Output != output {
		t.Errorf("unexpected completed data: %+v", completed)
	}
	if completed.CompletedAt == nil {
		t.Errorf("completed_at should be populated")
	}

	// List executions by device
	list, err := repo.ListExecutions(context.Background(), "dev-repo-1", 10)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(list) != 1 || list[0].ID != execID {
		t.Errorf("unexpected execution list count: %d", len(list))
	}
}

func TestTerminalSession_RepositoryLifecycle(t *testing.T) {
	_, d, _, _, repo, _ := newRemoteExecEnv(t)
	createTestDevice(t, d, "dev-term-1", "secret123", "TERM-HOST", "windows")

	now := time.Now().UTC()
	_, _ = d.Exec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"user-term-1", "termadmin", "hash", rbac.RoleAdmin, now, now)

	sessionID := remoteexec.NewID()
	sess := &remoteexec.TerminalSession{
		ID:         sessionID,
		DeviceID:   "dev-term-1",
		OperatorID: "user-term-1",
		ShellType:  "powershell",
		Status:     remoteexec.TermStatusActive,
		CreatedAt:  now,
	}

	if err := repo.CreateTerminalSession(context.Background(), sess); err != nil {
		t.Fatalf("create terminal session: %v", err)
	}

	active, err := repo.GetTerminalSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("get terminal session: %v", err)
	}
	if active.Status != remoteexec.TermStatusActive {
		t.Errorf("expected active status, got: %s", active.Status)
	}

	if err := repo.CloseTerminalSession(context.Background(), sessionID); err != nil {
		t.Fatalf("close terminal session: %v", err)
	}

	closed, err := repo.GetTerminalSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("get closed session: %v", err)
	}
	if closed.Status != remoteexec.TermStatusClosed || closed.ClosedAt == nil {
		t.Errorf("session not closed properly: %+v", closed)
	}
}

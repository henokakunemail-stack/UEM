package systempolicy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

const secretPw = "Endpoint-Admin-S3cret!"

type nopAudit struct{}

func (nopAudit) Log(context.Context, string, string, string, string, map[string]string) error {
	return nil
}

// policyEnv serves the real routes; role decides who is calling.
type policyEnv struct {
	t    *testing.T
	repo *Repository
	srv  *httptest.Server
	role atomic.Value
}

func newPolicyEnv(t *testing.T) *policyEnv {
	t.Helper()
	e := &policyEnv{t: t, repo: setupTestDB(t)}
	e.role.Store(rbac.RoleAdmin)
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(rbac.WithRole(r.Context(), e.role.Load().(string))))
		})
	}
	r := chi.NewRouter()
	NewHandler(e.repo, nil, nil, nopAudit{}, auth).Register(r)
	e.srv = httptest.NewServer(r)
	t.Cleanup(e.srv.Close)
	now := time.Now().UTC()
	e.repo.db.MustExec(`INSERT INTO devices (id, hostname, os_name, status, enrolled_at, device_secret_hash, created_at, updated_at)
		VALUES ('dev-1', 'PC-1', 'windows', 'online', ?, 'h', ?, ?)`, now, now, now)
	return e
}

func (e *policyEnv) do(method, path string, body any) (int, string) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out)
}

func (e *policyEnv) createAdminPolicy() string {
	e.t.Helper()
	code, body := e.do(http.MethodPost, "/api/system-policies", map[string]any{
		"name": "Local admin", "target_type": "all",
		"settings": map[string]any{
			"admin_account_enabled": true, "admin_account_username": "sysadmin", "admin_account_password": secretPw,
		},
	})
	if code != http.StatusCreated {
		e.t.Fatalf("create: %d %s", code, body)
	}
	if strings.Contains(body, secretPw) {
		e.t.Fatalf("create response carried the password: %s", body)
	}
	var p SystemPolicy
	_ = json.Unmarshal([]byte(body), &p)
	return p.ID
}

// No endpoint the console reads returns the managed admin password, to any
// role; they say a password is set instead.
func TestNoReadEndpointReturnsThePassword(t *testing.T) {
	e := newPolicyEnv(t)
	id := e.createAdminPolicy()
	// A state row as an earlier version stored it, password included: the
	// read endpoints must hide it even before the startup scrub has run.
	legacy, _ := json.Marshal(PolicySettings{AdminAccountEnabled: true, AdminAccountUsername: "sysadmin", AdminAccountPassword: secretPw})
	e.repo.db.MustExec(`INSERT INTO device_system_policy_states (device_id, policy_version, status, applied_settings_json, last_applied_at, error_message)
		VALUES ('dev-1', 'v', 'synced', ?, ?, '')`, string(legacy), time.Now().UTC())

	e.role.Store(rbac.RoleViewer)
	for _, path := range []string{
		"/api/system-policies",
		"/api/system-policies/" + id,
		"/api/devices/dev-1/system-policy",
		"/api/system-policies/states",
	} {
		code, body := e.do(http.MethodGet, path, nil)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		if strings.Contains(body, secretPw) {
			t.Errorf("%s returned the managed admin password to a viewer", path)
		}
	}
	_, body := e.do(http.MethodGet, "/api/system-policies/"+id, nil)
	if !strings.Contains(body, `"admin_account_password_set":true`) {
		t.Errorf("the policy does not say a password is set: %s", body)
	}

	e.role.Store(rbac.RoleAdmin)
	code, body := e.do(http.MethodPut, "/api/system-policies/"+id, map[string]any{"name": "Renamed"})
	if code != http.StatusOK || strings.Contains(body, secretPw) {
		t.Fatalf("update response: %d, carries password=%v", code, strings.Contains(body, secretPw))
	}
}

// The console never has the stored password, so a blank one on save means
// "keep it"; turning the managed account off clears it.
func TestBlankPasswordKeepsStoredAndDisablingClearsIt(t *testing.T) {
	e := newPolicyEnv(t)
	id := e.createAdminPolicy()
	settings := map[string]any{"admin_account_enabled": true, "admin_account_username": "sysadmin", "admin_account_password": ""}
	if code, body := e.do(http.MethodPut, "/api/system-policies/"+id, map[string]any{"settings": settings}); code != http.StatusOK {
		t.Fatalf("update: %d %s", code, body)
	}
	p, _ := e.repo.GetByID(context.Background(), id)
	if p.Settings.AdminAccountPassword != secretPw {
		t.Fatalf("a blank password on save replaced the stored one with %q", p.Settings.AdminAccountPassword)
	}

	settings["admin_account_enabled"] = false
	if code, body := e.do(http.MethodPut, "/api/system-policies/"+id, map[string]any{"settings": settings}); code != http.StatusOK {
		t.Fatalf("disable: %d %s", code, body)
	}
	p, _ = e.repo.GetByID(context.Background(), id)
	if p.Settings.AdminAccountPassword != "" {
		t.Fatal("turning the managed account off kept its password")
	}
}

func TestEnablingTheAccountWithoutAPasswordIsRejected(t *testing.T) {
	e := newPolicyEnv(t)
	code, body := e.do(http.MethodPost, "/api/system-policies", map[string]any{
		"name": "No pw", "target_type": "all",
		"settings": map[string]any{"admin_account_enabled": true, "admin_account_username": "sysadmin"},
	})
	if code != http.StatusBadRequest || !strings.Contains(body, "admin_password_required") {
		t.Fatalf("create: %d %s", code, body)
	}

	code, body = e.do(http.MethodPost, "/api/system-policies", map[string]any{"name": "Plain", "target_type": "all", "settings": map[string]any{}})
	if code != http.StatusCreated {
		t.Fatalf("create plain: %d %s", code, body)
	}
	var p SystemPolicy
	_ = json.Unmarshal([]byte(body), &p)
	code, body = e.do(http.MethodPut, "/api/system-policies/"+p.ID, map[string]any{
		"settings": map[string]any{"admin_account_enabled": true, "admin_account_username": "sysadmin"},
	})
	if code != http.StatusBadRequest || !strings.Contains(body, "admin_password_required") {
		t.Fatalf("update enabling without password: %d %s", code, body)
	}
}

// The agent's report used to echo the password into a row per device.
func TestReportedStateNeverStoresThePassword(t *testing.T) {
	e := newPolicyEnv(t)
	code, body := e.do(http.MethodPost, "/api/agent/devices/dev-1/system-policy/report", map[string]any{
		"policy_version": "v1", "status": "synced",
		"settings": map[string]any{"admin_account_enabled": true, "admin_account_username": "sysadmin", "admin_account_password": secretPw},
	})
	if code != http.StatusOK {
		t.Fatalf("report: %d %s", code, body)
	}
	var stored string
	if err := e.repo.db.Get(&stored, `SELECT applied_settings_json FROM device_system_policy_states WHERE device_id = 'dev-1'`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, secretPw) {
		t.Fatalf("the state row stored the password: %s", stored)
	}
	if !strings.Contains(stored, "sysadmin") {
		t.Fatalf("the rest of the applied settings was lost: %s", stored)
	}
}

func TestScrubRemovesPasswordsStoredByEarlierVersions(t *testing.T) {
	e := newPolicyEnv(t)
	legacy, _ := json.Marshal(PolicySettings{AdminAccountEnabled: true, AdminAccountUsername: "sysadmin", AdminAccountPassword: secretPw, USBStorageMode: USBStorageBlock})
	e.repo.db.MustExec(`INSERT INTO device_system_policy_states (device_id, policy_version, status, applied_settings_json, last_applied_at, error_message)
		VALUES ('dev-1', 'v', 'synced', ?, ?, '')`, string(legacy), time.Now().UTC())

	e.repo.ScrubStoredStatePasswords(context.Background())

	var stored string
	_ = e.repo.db.Get(&stored, `SELECT applied_settings_json FROM device_system_policy_states WHERE device_id = 'dev-1'`)
	if strings.Contains(stored, secretPw) {
		t.Fatalf("scrub left the password: %s", stored)
	}
	var s PolicySettings
	_ = json.Unmarshal([]byte(stored), &s)
	if s.USBStorageMode != USBStorageBlock || s.AdminAccountUsername != "sysadmin" {
		t.Fatalf("scrub damaged the other settings: %+v", s)
	}
}

// The version is shown to every role; it must not let anyone test password
// guesses offline, yet still change when the password changes.
func TestPolicyVersionIsNotAPasswordOracle(t *testing.T) {
	s := PolicySettings{AdminAccountEnabled: true, AdminAccountUsername: "sysadmin", AdminAccountPassword: secretPw}
	raw, _ := json.Marshal(s)
	sum := sha256.Sum256(raw)
	plainVersion := hex.EncodeToString(sum[:8])

	k1 := NewRepository(nil).WithVersionKey("secret-one").versionKey
	k2 := NewRepository(nil).WithVersionKey("secret-two").versionKey

	v := ComputePolicyVersion(s, k1)
	if v == plainVersion {
		t.Fatal("the version is a plain hash of the settings: a guess can be checked against it")
	}
	if v != ComputePolicyVersion(s, k1) {
		t.Fatal("the version is not stable for one key")
	}
	if v == ComputePolicyVersion(s, k2) {
		t.Fatal("the version does not depend on the server key")
	}
	changed := s
	changed.AdminAccountPassword = "Another-Password-1"
	if v == ComputePolicyVersion(changed, k1) {
		t.Fatal("a password change does not change the version, so agents would not re-apply it")
	}
}

// The agent still has to receive the password to set the account.
func TestDispatchStillCarriesThePassword(t *testing.T) {
	b, err := systemPolicyApplyEnvelope(&CompiledPolicy{Version: "v", Settings: PolicySettings{
		AdminAccountEnabled: true, AdminAccountUsername: "sysadmin", AdminAccountPassword: secretPw,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), secretPw) {
		t.Fatal("the dispatch to the agent lost the password; the account could not be set")
	}
}

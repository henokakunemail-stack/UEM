package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// loginEnv serves the real login handler with a counting password compare and
// a per-address limiter loose enough to reach the per-account lockout.
type loginEnv struct {
	t        *testing.T
	db       *sqlx.DB
	srv      *httptest.Server
	compares atomic.Int32
}

func newLoginEnv(t *testing.T) *loginEnv {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "login.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	e := &loginEnv{t: t, db: d}
	h := NewLoginHandler(d, NewJWTService(testSecret, 15*time.Minute, time.Hour)).
		WithRateLimiter(NewIPRateLimiter(1000, time.Minute, time.Minute))
	t.Cleanup(h.Close)
	h.compare = func(hash, plain string) error {
		e.compares.Add(1)
		return ComparePassword(hash, plain)
	}
	r := chi.NewRouter()
	h.Register(r)
	e.srv = httptest.NewServer(r)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *loginEnv) seed(username, password string, active bool) string {
	e.t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		e.t.Fatal(err)
	}
	id := "id-" + username
	now := time.Now().UTC()
	act := 1
	if !active {
		act = 0
	}
	e.db.MustExec(`INSERT INTO users (id, username, password_hash, role, is_active, created_at, updated_at) VALUES (?, ?, ?, 'admin', ?, ?, ?)`,
		id, username, hash, act, now, now)
	return id
}

type loginResult struct {
	status     int
	code       string
	retryAfter int
	header     string
}

func (e *loginEnv) login(username, password string) loginResult {
	e.t.Helper()
	b, _ := json.Marshal(map[string]string{"username": username, "password": password})
	res, err := http.Post(e.srv.URL+"/api/auth/login", "application/json", bytes.NewReader(b))
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Code       string `json:"code"`
		RetryAfter int    `json:"retry_after"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	return loginResult{status: res.StatusCode, code: body.Code, retryAfter: body.RetryAfter, header: res.Header.Get("Retry-After")}
}

func (e *loginEnv) auditCount(action, target string) int {
	e.t.Helper()
	var n int
	if err := e.db.Get(&n, `SELECT COUNT(*) FROM audit_logs WHERE action = ? AND target_id = ?`, action, target); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *loginEnv) failures(username string) int {
	var n int
	_ = e.db.Get(&n, `SELECT COALESCE(MAX(failures), 0) FROM login_failures WHERE username = ?`, username)
	return n
}

// A disabled account used to answer "account is disabled" before the password
// was checked, so any junk password confirmed the account existed.
func TestDisabledAccountWithAWrongPasswordLooksLikeBadCredentials(t *testing.T) {
	e := newLoginEnv(t)
	e.seed("leaver", "Right-Password-1", false)
	r := e.login("leaver", "wrong")
	if r.status != http.StatusUnauthorized || r.code != "invalid_credentials" {
		t.Fatalf("got %d %q, want 401 invalid_credentials", r.status, r.code)
	}
	if e.failures("leaver") != 1 {
		t.Fatal("a wrong password on a disabled account was not counted toward the lockout")
	}
}

func TestDisabledAccountWithTheRightPasswordSaysDisabled(t *testing.T) {
	e := newLoginEnv(t)
	id := e.seed("leaver", "Right-Password-1", false)
	r := e.login("leaver", "Right-Password-1")
	if r.status != http.StatusUnauthorized || r.code != "account_disabled" {
		t.Fatalf("got %d %q, want 401 account_disabled", r.status, r.code)
	}
	if e.auditCount("auth.login_failed", id) != 1 {
		t.Fatal("the refused sign-in of a disabled account was not audited")
	}
}

// A missing account must cost a bcrypt comparison like a real one, or
// response time tells which usernames exist.
func TestUnknownUserStillRunsOnePasswordComparison(t *testing.T) {
	e := newLoginEnv(t)
	r := e.login("nobody", "whatever")
	if r.status != http.StatusUnauthorized || r.code != "invalid_credentials" {
		t.Fatalf("got %d %q", r.status, r.code)
	}
	if got := e.compares.Load(); got != 1 {
		t.Fatalf("%d password comparisons for an unknown user, want 1", got)
	}
}

func TestFailedLoginIsAuditedOnlyForRealAccounts(t *testing.T) {
	e := newLoginEnv(t)
	id := e.seed("alice", "Right-Password-1", true)
	e.login("alice", "wrong")
	if e.auditCount("auth.login_failed", id) != 1 {
		t.Fatal("a wrong password for a real account was not audited")
	}
	e.login("random-name-123", "x")
	var n int
	_ = e.db.Get(&n, `SELECT COUNT(*) FROM audit_logs WHERE action = 'auth.login_failed'`)
	if n != 1 {
		t.Fatalf("%d login_failed rows, want 1: an unknown username must not write the audit trail", n)
	}
}

// The lock is audited once, when it happens; attempts against a locked
// account are refused early and add nothing.
func TestAccountLockIsAuditedExactlyOnce(t *testing.T) {
	e := newLoginEnv(t)
	id := e.seed("bob", "Right-Password-1", true)
	for i := 0; i < AccountMaxFailures; i++ {
		e.login("bob", "wrong")
	}
	if got := e.auditCount("auth.account_locked", id); got != 1 {
		t.Fatalf("%d account_locked rows after %d failures, want 1", got, AccountMaxFailures)
	}
	r := e.login("bob", "Right-Password-1")
	if r.status != http.StatusTooManyRequests || r.code != "too_many_attempts" {
		t.Fatalf("locked account: %d %q, want 429 too_many_attempts", r.status, r.code)
	}
	if strconv.Itoa(r.retryAfter) != r.header || r.retryAfter <= 0 {
		t.Fatalf("retry_after %d does not match the Retry-After header %q", r.retryAfter, r.header)
	}
	if got := e.auditCount("auth.account_locked", id); got != 1 {
		t.Fatalf("an attempt on a locked account added audit rows: %d", got)
	}
}

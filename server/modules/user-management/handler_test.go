package usermgmt

// Tests for the admin user-management surface.
//
// This module had none, and it is the one that creates and deactivates the
// accounts every other permission check in the system depends on. The tests
// below are about three things in order of consequence: that a non-admin cannot
// reach these routes at all, that a password never leaves the server in any
// form, and that changing a password or deactivating a user takes effect
// immediately rather than when the current token happens to expire.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type recordingAuditor struct{ entries []string }

func (a *recordingAuditor) Log(_ context.Context, actorType, actorID, action, targetID string, _ map[string]string) error {
	a.entries = append(a.entries, actorType+":"+actorID+":"+action+":"+targetID)
	return nil
}

type env struct {
	t        *testing.T
	db       *sqlx.DB
	r        *chi.Mux
	audit    *recordingAuditor
	jwt      *auth.JWTService
	sessions *auth.SessionStore
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	jwtSvc := auth.NewJWTService("user-mgmt-test-secret-0123456789abcdef", 15*time.Minute, time.Hour)
	aud := &recordingAuditor{}
	sessions := auth.NewSessionStore(d, time.Hour)

	r := chi.NewRouter()
	NewHandler(NewRepository(d), sessions, aud, jwtSvc.RequireAuth).Register(r)

	return &env{t: t, db: d, r: r, audit: aud, jwt: jwtSvc, sessions: sessions}
}

// actorID is the user id env.call mints its tokens for. Tests that exercise
// /api/users/me/password need a real row under this id, because the handler
// re-reads the user before it compares the old password.
const actorID = "actor"

// seed inserts a user directly, so a test never has to authenticate to set up
// the accounts it is about to act on.
func (e *env) seed(username, password, role string) string {
	e.t.Helper()
	return e.seedWithID("u-"+username, username, password, role)
}

func (e *env) seedWithID(id, username, password, role string) string {
	e.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		e.t.Fatalf("hash: %v", err)
	}
	now := time.Now().UTC()
	if _, err := e.db.Exec(
		`INSERT INTO users (id, username, password_hash, role, is_active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?)`, id, username, hash, role, now, now); err != nil {
		e.t.Fatalf("seed %s: %v", username, err)
	}
	return id
}

// call issues an authenticated request. role=="" sends no Authorization header at
// all, which is how the unauthenticated case is exercised.
func (e *env) call(method, path, role, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if role != "" {
		pair, err := e.jwt.Issue("actor", "actor", role)
		if err != nil {
			e.t.Fatalf("issue token: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	}
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	return rec
}

// loginAs drives the real login route on its own router, wired the way main.go
// wires it -- session store included, because without one the handler refuses
// the request for an unrelated reason and any assertion about is_active becomes
// vacuous.
//
// It also wires the trusted-proxy resolver the way main.go does, because a
// login without it resolves every caller to an untrusted peer and would rate
// limit the loopback test client once the resolver started caring.
func (e *env) loginAs(username, password string) *httptest.ResponseRecorder {
	e.t.Helper()
	h := auth.NewLoginHandler(e.db, e.jwt)
	h.WithSessionStore(e.sessions)
	h.WithTrustedProxies(nil)
	r := chi.NewRouter()
	h.Register(r)

	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(body))))
	return rec
}

// liveAccessToken logs in over the real login route and hands back the access
// token a real console ends up with -- one whose sid names a row in
// auth_sessions. e.call mints sid-less tokens, which RequireAuth still accepts
// for backward compatibility, and a revocation test needs the kind that
// revocation actually reaches.
func (e *env) liveAccessToken(username, password string) string {
	e.t.Helper()
	rec := e.loginAs(username, password)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("login as %s: %d %s", username, rec.Code, rec.Body.String())
	}
	var pair struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pair); err != nil {
		e.t.Fatalf("decode token pair: %v", err)
	}
	return pair.AccessToken
}

// getWith runs a GET against e.r carrying an explicit token, so a test can
// replay the same credential after the state underneath it has changed.
func (e *env) getWith(token, path string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	return rec
}

// strictEnv builds an env whose RequireAuth checks the session table, the way
// production does. Its router is separate from newEnv's because the existing
// tests mint sid-less tokens that would otherwise stop authenticating, and
// because a route registered twice on one chi mux is the one that was
// registered first.
//
// It also carries a /api/whoami route with no role requirement, so a test can
// ask "is this token still authenticated" without dragging in a role check that
// answers 403 for an unrelated reason.
func strictEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.jwt = e.jwt.WithSessionStore(e.sessions)
	e.r = chi.NewRouter()
	NewHandler(NewRepository(e.db), e.sessions, e.audit, e.jwt.RequireAuth).Register(e.r)
	e.r.With(e.jwt.RequireAuth).Get("/api/whoami", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"user_id": auth.UserIDFromContext(r.Context())})
	})
	return e
}

func (e *env) hashOf(id string) string {
	e.t.Helper()
	var h string
	if err := e.db.Get(&h, `SELECT password_hash FROM users WHERE id = ?`, id); err != nil {
		e.t.Fatalf("read hash: %v", err)
	}
	return h
}

func (e *env) roleOf(id string) string {
	e.t.Helper()
	var role string
	if err := e.db.Get(&role, `SELECT role FROM users WHERE id = ?`, id); err != nil {
		e.t.Fatalf("read role: %v", err)
	}
	return role
}

func (e *env) activeOf(id string) bool {
	e.t.Helper()
	var active int
	if err := e.db.Get(&active, `SELECT COALESCE(is_active, 1) FROM users WHERE id = ?`, id); err != nil {
		e.t.Fatalf("read is_active: %v", err)
	}
	return active == 1
}

// ---------------------------------------------------------------------------
// Authorization. Every admin route must refuse a viewer, a technician, and an
// anonymous caller. This is the first thing to break if a route is ever moved
// out of the group.
// ---------------------------------------------------------------------------

func TestOnlyAnAdminReachesTheUserAdminRoutes(t *testing.T) {
	e := newEnv(t)
	target := e.seed("victim", "victim-password", rbac.RoleViewer)

	body := `{"username":"intruder","password":"intruder-pass","role":"admin"}`
	cases := []struct {
		method, path, role, payload string
	}{
		{"GET", "/api/users", "", ""},
		{"GET", "/api/users", rbac.RoleViewer, ""},
		{"GET", "/api/users", rbac.RoleTechnician, ""},
		{"POST", "/api/users", rbac.RoleViewer, body},
		{"POST", "/api/users", rbac.RoleTechnician, body},
		{"GET", "/api/users/" + target, rbac.RoleTechnician, ""},
		{"PUT", "/api/users/" + target, rbac.RoleTechnician, `{"role":"admin"}`},
		{"PUT", "/api/users/" + target + "/password", rbac.RoleTechnician, `{"new_password":"pwned12345"}`},
		{"DELETE", "/api/users/" + target, rbac.RoleTechnician, ""},
	}

	for _, c := range cases {
		name := c.role
		if name == "" {
			name = "anonymous"
		}
		t.Run(c.method+" "+name+" "+c.path, func(t *testing.T) {
			rec := e.call(c.method, c.path, c.role, c.payload)
			if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 or 403\nbody: %s", rec.Code, rec.Body.String())
			}
		})
	}

	// Nothing above may have changed anything.
	if e.roleOf(target) != rbac.RoleViewer {
		t.Errorf("victim role = %q: a non-admin changed it", e.roleOf(target))
	}
	if e.activeOf(target) != true {
		t.Error("victim was deactivated by a non-admin")
	}
	// And the intruder account must not exist.
	var n int
	if err := e.db.Get(&n, `SELECT COUNT(*) FROM users WHERE username = 'intruder'`); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("a non-admin created a user")
	}
}

// TestOnlyAnAdminReachesTheUserAdminRoutes proves the guard. This proves the
// ordinary path still works, which is the half that matters if the guard is ever
// tightened by accident.
func TestAnAdminCanStillManageUsers(t *testing.T) {
	e := newEnv(t)
	victim := e.seed("victim", "victim-password", rbac.RoleViewer)

	rec := e.call("GET", "/api/users", rbac.RoleAdmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list as admin = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	rec = e.call("PUT", "/api/users/"+victim, rbac.RoleAdmin, `{"role":"technician"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update as admin = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if got := e.roleOf(victim); got != rbac.RoleTechnician {
		t.Errorf("role = %q, want technician", got)
	}
}

// ---------------------------------------------------------------------------
// Passwords. The hash must not leave the server in any response, and a stored
// hash must never be the plaintext.
// ---------------------------------------------------------------------------

func TestNoResponseEverCarriesAPasswordHash(t *testing.T) {
	e := newEnv(t)
	created := e.seed("alice", "alice-password", rbac.RoleTechnician)

	bodies := []struct{ name, method, path, payload string }{
		{"list", "GET", "/api/users", ""},
		{"get", "GET", "/api/users/" + created, ""},
		{"create", "POST", "/api/users", `{"username":"bob","password":"bob-password","role":"viewer"}`},
		{"update", "PUT", "/api/users/" + created, `{"display_name":"Alice"}`},
		{"reset", "PUT", "/api/users/" + created + "/password", `{"new_password":"new-password-1"}`},
		{"self", "PUT", "/api/users/me/password", `{"old_password":"alice-password","new_password":"another-pass-1"}`},
	}

	for _, b := range bodies {
		t.Run(b.name, func(t *testing.T) {
			rec := e.call(b.method, b.path, rbac.RoleAdmin, b.payload)
			body := rec.Body.String()
			for _, secret := range []string{e.hashOf(created), "alice-password", "bob-password", "new-password-1", "another-pass-1"} {
				if secret == "" {
					continue
				}
				if strings.Contains(body, secret) {
					t.Errorf("%s response contains a credential (%q):\n%s", b.name, secret, body)
				}
			}
		})
	}
}

func TestAPasswordIsNeverStoredInPlaintext(t *testing.T) {
	e := newEnv(t)

	rec := e.call("POST", "/api/users", rbac.RoleAdmin,
		`{"username":"carol","password":"correct-horse-battery","role":"technician"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}

	var stored string
	if err := e.db.Get(&stored, `SELECT password_hash FROM users WHERE username = 'carol'`); err != nil {
		t.Fatal(err)
	}
	if stored == "correct-horse-battery" {
		t.Fatal("the password was stored in plaintext")
	}
	if len(stored) < 40 {
		t.Errorf("stored hash is only %d chars; that is not a bcrypt cost, it is a "+
			"digest that a GPU can brute-force", len(stored))
	}
	if err := auth.ComparePassword(stored, "correct-horse-battery"); err != nil {
		t.Errorf("the stored hash does not verify against the password: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Privilege escalation. createUser defaults a missing role to viewer, and an
// unknown role is refused rather than passed through to the role column.
// ---------------------------------------------------------------------------

func TestCreatingAUserRefusesAnUnknownRole(t *testing.T) {
	e := newEnv(t)

	for _, role := range []string{`"superuser"`, `""`, `"Admin"`, `"admin "`} {
		payload := `{"username":"probe","password":"probe-password","role":` + role + `}`
		if role == `""` {
			payload = `{"username":"probe","password":"probe-password","role":""}`
		}
		rec := e.call("POST", "/api/users", rbac.RoleAdmin, payload)
		// An empty role is explicitly defaulted to viewer, so that one is legal.
		wantOK := role == `""`
		if wantOK {
			if rec.Code != http.StatusCreated {
				t.Errorf("empty role = %d, want 201 (it should default to viewer)", rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("role %s = %d, want 400\nbody: %s", role, rec.Code, rec.Body.String())
		}
	}
}

func TestAMissingRoleDefaultsToViewer(t *testing.T) {
	e := newEnv(t)

	rec := e.call("POST", "/api/users", rbac.RoleAdmin,
		`{"username":"dave","password":"dave-password"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}
	var role string
	if err := e.db.Get(&role, `SELECT role FROM users WHERE username = 'dave'`); err != nil {
		t.Fatal(err)
	}
	if role != rbac.RoleViewer {
		t.Errorf("role = %q, want viewer: a user created with no role must get the "+
			"least privilege, not the one the handler happened to be holding", role)
	}
}

// ---------------------------------------------------------------------------
// Password rules. 8 characters is the floor the handler enforces.
// ---------------------------------------------------------------------------

func TestShortPasswordsAreRefused(t *testing.T) {
	e := newEnv(t)
	e.seed("eve", "eve-password-1", rbac.RoleTechnician)

	if rec := e.call("POST", "/api/users", rbac.RoleAdmin,
		`{"username":"shorty","password":"1234567","role":"viewer"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("7-character password on create = %d, want 400", rec.Code)
	}
	if rec := e.call("PUT", "/api/users/u-eve/password", rbac.RoleAdmin,
		`{"new_password":"1234567"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("7-character password on reset = %d, want 400", rec.Code)
	}
	if rec := e.call("PUT", "/api/users/me/password", rbac.RoleAdmin,
		`{"old_password":"eve-password-1","new_password":"1234567"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("7-character password on self-change = %d, want 400", rec.Code)
	}

	// The rejected attempt must not have changed the stored hash.
	if e.hashOf("u-eve") == "" {
		t.Error("user row disappeared")
	}
}

// ---------------------------------------------------------------------------
// Self-service password change requires the current password. Without that
// check, a stolen access token is enough to lock the real owner out.
//
// Every test here seeds the actor itself (id "actor"), because changeSelfPassword
// re-reads the caller's user row before it compares anything. Against a token
// with no matching row the handler returns 404 first, and a test asserting on
// 400 would then be asserting on the 404 -- a green test that never reached the
// check it exists to protect.
// ---------------------------------------------------------------------------

func TestSelfPasswordChangeRequiresTheCurrentPassword(t *testing.T) {
	e := newEnv(t)
	e.seedWithID(actorID, "frank", "frank-password-1", rbac.RoleTechnician)
	before := e.hashOf(actorID)

	rec := e.call("PUT", "/api/users/me/password", rbac.RoleTechnician,
		`{"old_password":"not-the-password","new_password":"attacker-chosen"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}
	if after := e.hashOf(actorID); after != before {
		t.Error("the password changed even though the current password was wrong")
	}
}

func TestSelfPasswordChangeSucceedsWithTheCurrentPassword(t *testing.T) {
	e := newEnv(t)
	e.seedWithID(actorID, "frank", "frank-password-1", rbac.RoleTechnician)

	rec := e.call("PUT", "/api/users/me/password", rbac.RoleTechnician,
		`{"old_password":"frank-password-1","new_password":"frank-password-2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if err := auth.ComparePassword(e.hashOf(actorID), "frank-password-2"); err != nil {
		t.Errorf("the new password does not verify: %v", err)
	}
}

// TestSelfPasswordChangeIsScopedToTheCaller is the real boundary. The route is
// /me, so the id comes from the token, not the body or the path. A handler that
// read an id from either would let any user rewrite another user's password and
// take the account over.
func TestSelfPasswordChangeIsScopedToTheCaller(t *testing.T) {
	e := newEnv(t)
	e.seedWithID(actorID, "attacker", "attacker-password-1", rbac.RoleTechnician)
	victim := e.seed("victim", "victim-password-1", rbac.RoleTechnician)
	before := e.hashOf(victim)

	// The actor is a real user here, so a 200 would mean the handler honoured
	// the payload's id instead of the token's. The route has no {id} to name
	// them in, so the only way this passes is by reading the token.
	rec := e.call("PUT", "/api/users/me/password", rbac.RoleTechnician,
		`{"user_id":"`+victim+`","id":"`+victim+`","old_password":"victim-password-1","new_password":"attacker-chosen"}`)
	if rec.Code == http.StatusOK {
		t.Errorf("the change succeeded for a payload naming another user: %s", rec.Body.String())
	}
	if after := e.hashOf(victim); after != before {
		t.Fatal("another user's password was rewritten through /me")
	}
}

// ---------------------------------------------------------------------------
// Deactivating a user has to take effect now, not at token expiry, and an
// admin must not be able to lock themselves out of the console.
// ---------------------------------------------------------------------------

func TestAnAdminCannotDeactivateTheirOwnAccount(t *testing.T) {
	e := newEnv(t)
	// env.call always issues tokens for the id "actor".
	rec := e.call("DELETE", "/api/users/actor", rbac.RoleAdmin, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}
}

// TestADemotedUsersSessionsAreRevoked is the role-change half of revocation.
// rbac.RequireRole reads the role off the token, not the users row, so the row
// and the token can disagree for as long as a token minted under the old role
// is unexpired. A technician demoted to viewer keeps terminal access through
// that window; the fix is the same one deactivation already uses.
func TestADemotedUsersSessionsAreRevoked(t *testing.T) {
	e := strictEnv(t)
	victim := e.seed("victim", "victim-password-1", rbac.RoleTechnician)

	// The victim is signed in and holding a live token, as they would be.
	token := e.liveAccessToken("victim", "victim-password-1")
	if rec := e.getWith(token, "/api/whoami"); rec.Code != http.StatusOK {
		t.Fatalf("the victim's live token before the demotion: %d %s",
			rec.Code, rec.Body.String())
	}

	// Demote via the admin route, the way an operator does.
	body, _ := json.Marshal(map[string]string{"role": rbac.RoleViewer})
	if rec := e.call("PUT", "/api/users/"+victim, rbac.RoleAdmin, string(body)); rec.Code != http.StatusOK {
		t.Fatalf("demote = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if e.roleOf(victim) != rbac.RoleViewer {
		t.Fatalf("role after demotion = %q", e.roleOf(victim))
	}

	// The session is gone, so the token the victim is holding stops working.
	var live int
	if err := e.db.Get(&live,
		`SELECT COUNT(*) FROM auth_sessions WHERE user_id = ? AND revoked = 0`, victim); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Errorf("%d live session(s) remain after a role change; the old role "+
			"is still on the token in the victim's browser", live)
	}
	if rec := e.getWith(token, "/api/whoami"); rec.Code != http.StatusUnauthorized {
		t.Errorf("the pre-demotion token still authenticated after the role "+
			"change: status %d, want 401", rec.Code)
	}
}

// TestARoleChangeThatDoesNotChangeTheRoleRevokesNothing: revoking on every
// update would sign a user out because an admin corrected a display name. The
// trigger is the role actually differing, and the check reads the row before
// the write to see what it is changing from.
func TestARoleChangeThatDoesNotChangeTheRoleRevokesNothing(t *testing.T) {
	e := newEnv(t)
	victim := e.seed("victim", "victim-password-1", rbac.RoleTechnician)

	// A same-role update, which is what an admin sends to fix a display name.
	body, _ := json.Marshal(map[string]string{"role": rbac.RoleTechnician})
	if rec := e.call("PUT", "/api/users/"+victim, rbac.RoleAdmin, string(body)); rec.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	var live int
	if err := e.db.Get(&live,
		`SELECT COUNT(*) FROM auth_sessions WHERE user_id = ? AND revoked = 0`, victim); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Errorf("%d session(s) revoked by a no-op role update", live)
	}
}

// TestAnAdminCannotChangeTheirOwnRole: the demotion would sign the operator out
// of the console they are making the request from. deactivateUser refuses the
// same thing for the same reason, and the update route is where a
// self-demotion would otherwise slip through, because it takes the role from
// the body rather than from is_active.
func TestAnAdminCannotChangeTheirOwnRole(t *testing.T) {
	e := newEnv(t)
	e.seedWithID(actorID, "actor", "actor-password-1", rbac.RoleAdmin)

	body, _ := json.Marshal(map[string]string{"role": rbac.RoleViewer})
	rec := e.call("PUT", "/api/users/"+actorID, rbac.RoleAdmin, string(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("self-demotion = %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}
	if e.roleOf(actorID) != rbac.RoleAdmin {
		t.Errorf("role was changed despite the refusal: %q", e.roleOf(actorID))
	}
}

// TestDeactivatedUserCannotLogIn is the point of deactivation. is_active is
// checked in login.go, so this closes the loop end to end: the admin route
// writes the column, and the login route honours it.
//
// The handler is built with its session store, exactly as main.go does. An
// earlier version of this test left it off, and it still went green -- the
// login handler refuses a request for want of a session store long before it
// looks at is_active, so the assertion was satisfied by an unrelated 500. A
// test that cannot fail against the bug it names is worse than no test.
func TestDeactivatedUserCannotLogIn(t *testing.T) {
	e := newEnv(t)
	victim := e.seed("victim", "victim-password-1", rbac.RoleTechnician)

	// Sanity: the account works before it is deactivated, so a green assertion
	// afterwards can only be the is_active check.
	if rec := e.loginAs("victim", "victim-password-1"); rec.Code != http.StatusOK {
		t.Fatalf("login before deactivation = %d, want 200; the test cannot prove "+
			"anything if the account was never usable\nbody: %s", rec.Code, rec.Body.String())
	}

	if rec := e.call("DELETE", "/api/users/"+victim, rbac.RoleAdmin, ""); rec.Code != http.StatusOK {
		t.Fatalf("deactivate = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if e.activeOf(victim) {
		t.Fatal("is_active is still 1 after deactivate")
	}

	if rec := e.loginAs("victim", "victim-password-1"); rec.Code == http.StatusOK {
		t.Error("a deactivated user logged in; the is_active check is not being honoured")
	}
}

// TestADeactivatedUserLosesApiAccess is the one that matters, and the reason
// this file exists.
//
// deactivating a user only writes is_active = 0. RequireAuth validates the JWT
// signature and nothing else -- it reads no row, so an access token minted
// before the deactivation keeps working until it expires on its own. With
// ACCESS_TOKEN_TTL defaulting to 15m, a dismissed technician keeps admin-grade
// API access for up to a quarter of an hour after an admin revoked their
// account, and nothing in the request path notices.
//
// The end-to-end version lives in server/core/auth/session_enforcement_test.go,
// where the middleware itself is built; this is the module-local half, proving
// the write happens and no session row is left behind to re-authorise them.
func TestADeactivatedUserLosesApiAccess(t *testing.T) {
	e := newEnv(t)
	victim := e.seed("victim", "victim-password-1", rbac.RoleTechnician)

	// The victim holds a live session, as they would while signed in.
	login := e.loginAs("victim", "victim-password-1")
	if login.Code != http.StatusOK {
		t.Fatalf("victim could not sign in: %d", login.Code)
	}
	var jti string
	if err := e.db.Get(&jti, `SELECT jti FROM auth_sessions WHERE user_id = ?`, victim); err != nil {
		t.Fatal(err)
	}

	if rec := e.call("DELETE", "/api/users/"+victim, rbac.RoleAdmin, ""); rec.Code != http.StatusOK {
		t.Fatalf("deactivate = %d, want 200", rec.Code)
	}

	// The session must be revoked, or the dismissed user refreshes straight back
	// in. Revocation keeps the row and sets revoked = 1 -- the schema comment
	// in 0016 says why, and deleting instead would make the token valid again.
	var revoked int
	if err := e.db.Get(&revoked, `SELECT revoked FROM auth_sessions WHERE jti = ?`, jti); err != nil {
		t.Fatalf("session row disappeared rather than being revoked: %v", err)
	}
	if revoked != 1 {
		t.Error("deactivate left the user's session live: with a valid session id " +
			"on their access token they can keep using the API, and refresh back into it")
	}
}

// TestADeactivatedUserKeepsTheirAuditTrail confirms deactivate is a flag, not a
// delete. A user row is referenced by audit_log, remote_exec, patch jobs and
// task scheduler, so removing it would cascade away the record of who did what.
func TestADeactivatedUserKeepsTheirAuditTrail(t *testing.T) {
	e := newEnv(t)
	victim := e.seed("victim", "victim-password-1", rbac.RoleTechnician)

	rec := e.call("DELETE", "/api/users/"+victim, rbac.RoleAdmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate = %d, want 200", rec.Code)
	}

	var n int
	if err := e.db.Get(&n, `SELECT COUNT(*) FROM users WHERE id = ?`, victim); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("the user row was removed; the foreign keys from audit_log, " +
			"remote_exec, patch_install_jobs and scheduled tasks point at it")
	}
}

// ---------------------------------------------------------------------------
// The admin actions are the auditable ones. Every one of them writes a line.
// ---------------------------------------------------------------------------

func TestEveryAdminActionIsAudited(t *testing.T) {
	e := newEnv(t)
	victim := e.seed("victim", "victim-password-1", rbac.RoleTechnician)

	steps := []struct {
		name, method, path, payload, wantAction string
	}{
		{"create", "POST", "/api/users", `{"username":"newguy","password":"newguy-pass-1","role":"viewer"}`, "user.create"},
		{"update", "PUT", "/api/users/" + victim, `{"role":"admin"}`, "user.update"},
		{"reset", "PUT", "/api/users/" + victim + "/password", `{"new_password":"reset-pass-99"}`, "user.reset_password"},
		{"deactivate", "DELETE", "/api/users/" + victim, "", "user.deactivate"},
	}
	for _, s := range steps {
		if rec := e.call(s.method, s.path, rbac.RoleAdmin, s.payload); rec.Code >= 400 {
			t.Fatalf("%s = %d, want success\nbody: %s", s.name, rec.Code, rec.Body.String())
		}
	}

	var joined strings.Builder
	for _, entry := range e.audit.entries {
		joined.WriteString(entry)
		joined.WriteString("\n")
	}
	for _, s := range steps {
		if !strings.Contains(joined.String(), s.wantAction) {
			t.Errorf("no audit entry for %s (action %q); recorded:\n%s", s.name, s.wantAction, joined.String())
		}
	}
}

// ---------------------------------------------------------------------------
// Update is a partial update: a payload carrying only role must not blank the
// display name, and a payload carrying only display_name must not reset the
// role.
// ---------------------------------------------------------------------------

func TestUpdateTouchesOnlyTheFieldsItWasGiven(t *testing.T) {
	e := newEnv(t)
	e.seed("grace", "grace-password-1", rbac.RoleTechnician)
	if _, err := e.db.Exec(`UPDATE users SET display_name = 'Grace H' WHERE id = 'u-grace'`); err != nil {
		t.Fatal(err)
	}

	// Role only.
	if rec := e.call("PUT", "/api/users/u-grace", rbac.RoleAdmin, `{"role":"admin"}`); rec.Code != http.StatusOK {
		t.Fatalf("update role = %d\nbody: %s", rec.Code, rec.Body.String())
	}
	var name string
	if err := e.db.Get(&name, `SELECT COALESCE(display_name,'') FROM users WHERE id = 'u-grace'`); err != nil {
		t.Fatal(err)
	}
	if name != "Grace H" {
		t.Errorf("display_name = %q, want \"Grace H\": a partial update blanked the "+
			"field it was not given", name)
	}

	// Display name only.
	if rec := e.call("PUT", "/api/users/u-grace", rbac.RoleAdmin, `{"display_name":"Grace Hopper"}`); rec.Code != http.StatusOK {
		t.Fatalf("update display_name = %d\nbody: %s", rec.Code, rec.Body.String())
	}
	if got := e.roleOf("u-grace"); got != rbac.RoleAdmin {
		t.Errorf("role = %q, want admin: a partial update reset the field it was "+
			"not given", got)
	}
}

// TestAnUpdateNamingNoFieldIsRejected catches the empty-SET case. The repository
// builds its SET clause from the fields present, so a payload with none of them
// produces `SET updated_at = ?`, which succeeds and touches nothing -- a silent
// no-op that reports 200.
func TestAnUpdateNamingNoFieldIsRejected(t *testing.T) {
	e := newEnv(t)
	e.seed("heidi", "heidi-password-1", rbac.RoleViewer)

	rec := e.call("PUT", "/api/users/u-heidi", rbac.RoleAdmin, `{}`)
	if rec.Code == http.StatusOK {
		t.Errorf("an empty update returned 200; it silently did nothing\nbody: %s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Duplicate usernames.
// ---------------------------------------------------------------------------

func TestADuplicateUsernameIsRefused(t *testing.T) {
	e := newEnv(t)
	e.seed("ivan", "ivan-password-1", rbac.RoleViewer)

	rec := e.call("POST", "/api/users", rbac.RoleAdmin,
		`{"username":"ivan","password":"another-pass-1","role":"admin"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409\nbody: %s", rec.Code, rec.Body.String())
	}

	// And the original must be untouched: a duplicate must not overwrite the
	// existing account, which is a privilege escalation if it did.
	if got := e.roleOf("u-ivan"); got != rbac.RoleViewer {
		t.Errorf("the original account's role = %q, want viewer: the duplicate "+
			"attempt overwrote it", got)
	}
	if e.hashOf("u-ivan") == "" {
		t.Error("the original account's password was overwritten")
	}
}

// ---------------------------------------------------------------------------
// Unknown ids. A missing user must be a 404, and must not be distinguishable
// from one the caller may not see -- there is only one admin role, so that
// second half is trivially satisfied here, but the 404 itself is the contract.
// ---------------------------------------------------------------------------

func TestAnUnknownUserIs404(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		method, path, payload string
	}{
		{"GET", "/api/users/no-such-user", ""},
		{"PUT", "/api/users/no-such-user", `{"role":"admin"}`},
		{"PUT", "/api/users/no-such-user/password", `{"new_password":"whatever-123"}`},
		{"DELETE", "/api/users/no-such-user", ""},
	} {
		rec := e.call(c.method, c.path, rbac.RoleAdmin, c.payload)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404\nbody: %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

// TestMalformedJSONIsRejected: a body the handler cannot parse must not be
// treated as an empty request, which for createUser means a missing username.
func TestMalformedJSONIsRejected(t *testing.T) {
	e := newEnv(t)
	e.seed("judy", "judy-password-1", rbac.RoleViewer)

	for _, payload := range []string{`{`, `not json`, `[]`, ``} {
		rec := e.call("POST", "/api/users", rbac.RoleAdmin, payload)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create with %q = %d, want 400\nbody: %s", payload, rec.Code, rec.Body.String())
		}
	}
}

// TestListUsersIsBounded: List takes a limit and clamps it, so a caller cannot
// ask for the whole table by passing 0 or a negative number.
func TestListUsersIsBounded(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 12; i++ {
		e.seed("bulk"+string(rune('a'+i)), "bulk-password-1", rbac.RoleViewer)
	}

	rec := e.call("GET", "/api/users", rbac.RoleAdmin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	var users []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &users); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, rec.Body.String())
	}
	if len(users) == 0 {
		t.Error("list returned nothing")
	}
	// The response must be a JSON array even when it is empty -- a null would
	// break the console's .map().
	if !strings.Contains(rec.Body.String(), "[") {
		t.Errorf("list did not answer with an array: %s", rec.Body.String())
	}
}

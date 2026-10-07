package auth

// Session tests. Every case here is a way a refresh token should stop working,
// so the assertions are about refusal as much as about success: a test that
// only proves the happy path would pass against the stateless code these tests
// replace.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

const testSecret = "session-test-secret-0123456789abcdef"

// env is one isolated server: a real migrated database, a real router, and the
// real handler. Nothing is stubbed, so a test cannot pass by mocking the
// behaviour it is meant to prove.
type env struct {
	t   *testing.T
	db  *sqlx.DB
	jwt *JWTService
	srv *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	jwtSvc := NewJWTService(testSecret, 15*time.Minute, time.Hour)
	r := chi.NewRouter()
	h := NewLoginHandler(d, jwtSvc)
	t.Cleanup(h.Close)
	h.Register(r)

	// A stand-in for the fleet's authenticated routes, so RequireAuth can be
	// exercised over real HTTP rather than by calling the middleware directly.
	r.Group(func(pr chi.Router) {
		pr.Use(jwtSvc.RequireAuth)
		pr.Get("/api/whoami", func(w http.ResponseWriter, req *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{
				"user_id": UserIDFromContext(req.Context()),
			})
		})
	})

	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return &env{t: t, db: d, jwt: jwtSvc, srv: ts}
}

func (e *env) seedUser(username, password, role string) string {
	e.t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		e.t.Fatalf("hash password: %v", err)
	}
	id := fmt.Sprintf("user-%s-%d", username, time.Now().UnixNano())
	now := time.Now().UTC()
	if _, err := e.db.Exec(
		`INSERT INTO users (id, username, password_hash, role, is_active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?)`, id, username, hash, role, now, now); err != nil {
		e.t.Fatalf("seed user %s: %v", username, err)
	}
	return id
}

type tokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

type httpResult struct {
	code int
	body []byte
}

func (e *env) do(method, path, bearer string, body any) httpResult {
	e.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rdr)
	if err != nil {
		e.t.Fatalf("build request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(res.Body); err != nil {
		e.t.Fatalf("read body: %v", err)
	}
	return httpResult{code: res.StatusCode, body: buf.Bytes()}
}

func (e *env) login(username, password string) tokenPair {
	e.t.Helper()
	res := e.do(http.MethodPost, "/api/auth/login", "",
		map[string]string{"username": username, "password": password})
	if res.code != http.StatusOK {
		e.t.Fatalf("login %s: status %d, body %s", username, res.code, res.body)
	}
	var pair tokenPair
	if err := json.Unmarshal(res.body, &pair); err != nil {
		e.t.Fatalf("decode login response: %v (body=%s)", err, res.body)
	}
	if pair.RefreshToken == "" {
		e.t.Fatal("login returned no refresh token")
	}
	return pair
}

func (e *env) refresh(refreshToken string) httpResult {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/auth/refresh", "",
		map[string]string{"refresh_token": refreshToken})
}

func (e *env) refreshOK(refreshToken string) tokenPair {
	e.t.Helper()
	res := e.refresh(refreshToken)
	if res.code != http.StatusOK {
		e.t.Fatalf("refresh: status %d, body %s", res.code, res.body)
	}
	var pair tokenPair
	if err := json.Unmarshal(res.body, &pair); err != nil {
		e.t.Fatalf("decode refresh response: %v (body=%s)", err, res.body)
	}
	return pair
}

// jtiOf reads the session id out of a refresh token without going through the
// HTTP layer, for assertions about which row a call touched.
func (e *env) jtiOf(token string) string {
	e.t.Helper()
	claims, err := e.jwt.ParseKind(token, KindRefresh)
	if err != nil {
		e.t.Fatalf("parse refresh token: %v", err)
	}
	if claims.ID == "" {
		e.t.Fatal("refresh token carries no jti — no session row can be created for it")
	}
	return claims.ID
}

func (e *env) sessionRow(jti string) Session {
	e.t.Helper()
	row, err := NewSessionStore(e.db, time.Hour).Get(context.Background(), jti)
	if err != nil {
		e.t.Fatalf("read session %s: %v", jti, err)
	}
	return *row
}

func (e *env) liveSessionCount(userID string) int {
	e.t.Helper()
	var n int
	if err := e.db.Get(&n, `
		SELECT COUNT(*) FROM auth_sessions
		WHERE user_id = ? AND revoked = 0 AND expires_at > ?`, userID, time.Now().UTC()); err != nil {
		e.t.Fatalf("count live sessions: %v", err)
	}
	return n
}

// signLegacyAccess produces a token shaped like the ones issued before Kind
// existed: no "knd" claim, no session id, no jti. Signing it here rather than
// reusing a checked-in string is what makes the backward-compatibility test
// real — a hardcoded token would go stale the first time the secret or the TTL
// changed, and would quietly stop testing anything.
func signLegacyAccess(t *testing.T, secret, userID, username, role string) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
			Subject:   userID,
		},
	})
	signed, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign legacy token: %v", err)
	}
	return signed
}

// TestLoginCreatesSessionRow proves the login path writes the row at all. A
// refresh implementation that validated against a table nobody populates would
// reject every real user on their first refresh.
func TestLoginCreatesSessionRow(t *testing.T) {
	e := newEnv(t)
	userID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	pair := e.login("alice", "pw-alice")
	jti := e.jtiOf(pair.RefreshToken)

	row := e.sessionRow(jti)
	if row.UserID != userID {
		t.Errorf("session row user_id = %q, want %q", row.UserID, userID)
	}
	if row.Role != rbac.RoleAdmin {
		t.Errorf("session row role = %q, want %q", row.Role, rbac.RoleAdmin)
	}
	if row.Revoked {
		t.Error("a freshly issued session must not be marked revoked")
	}
	if !row.ExpiresAt.After(time.Now().UTC()) {
		t.Errorf("session expires_at %v is not in the future", row.ExpiresAt)
	}
}

// TestRefreshRotatesSession is the core guarantee: a refresh token is
// single-use. The old one must stop working, the new one must work, and the
// console must be able to tell "refreshed" from "logged out" via rotated_to.
func TestRefreshRotatesSession(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	first := e.login("alice", "pw-alice")
	oldJTI := e.jtiOf(first.RefreshToken)

	second := e.refreshOK(first.RefreshToken)
	newJTI := e.jtiOf(second.RefreshToken)

	if newJTI == oldJTI {
		t.Fatal("refresh reused the old jti — a rotated token is not rotated")
	}

	old := e.sessionRow(oldJTI)
	if !old.Revoked {
		t.Error("old session row is not revoked after rotation")
	}
	if !old.RotatedTo.Valid || old.RotatedTo.String != newJTI {
		t.Errorf("old row rotated_to = %v, want %q", old.RotatedTo, newJTI)
	}

	if e.sessionRow(newJTI).Revoked {
		t.Error("new session row must be live")
	}

	// The replacement carries the identity of the row it replaced; a rotation
	// that changed any of it would be a privilege escalation path.
	newRow := e.sessionRow(newJTI)
	if newRow.UserID != old.UserID || newRow.Username != old.Username || newRow.Role != old.Role {
		t.Errorf("rotation changed identity: old=%+v new=%+v", old, newRow)
	}

	// The new token is usable, and the old one is not.
	if res := e.refresh(second.RefreshToken); res.code != http.StatusOK {
		t.Errorf("second refresh of the new token: status %d, want 200", res.code)
	}
	if res := e.refresh(first.RefreshToken); res.code != http.StatusUnauthorized {
		t.Errorf("reused old refresh token: status %d, want 401", res.code)
	}
}

// TestReplayedRefreshTokenRevokesEverySession is the theft response. Presenting
// a token that already worked means either the legitimate client retried or
// someone else is holding a copy, and the server cannot tell which — so it must
// assume the worst and drop every session the user has.
func TestReplayedRefreshTokenRevokesEverySession(t *testing.T) {
	e := newEnv(t)
	userID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	// Three browsers, as a real operator would have.
	first := e.login("alice", "pw-alice")
	second := e.login("alice", "pw-alice")
	third := e.login("alice", "pw-alice")
	if n := e.liveSessionCount(userID); n != 3 {
		t.Fatalf("expected 3 live sessions, got %d", n)
	}

	// The legitimate browser rotates. The attacker's copy of the old token then
	// arrives, which is exactly the case the revocation exists for.
	e.refreshOK(first.RefreshToken)

	res := e.refresh(first.RefreshToken)
	if res.code != http.StatusUnauthorized {
		t.Errorf("replayed refresh token: status %d, want 401", res.code)
	}

	if n := e.liveSessionCount(userID); n != 0 {
		t.Errorf("after a replay %d session(s) survived; every session for the user must be revoked", n)
	}
	// The other two browsers' tokens are collateral damage, on purpose.
	for name, tok := range map[string]string{"second": second.RefreshToken, "third": third.RefreshToken} {
		if got := e.refresh(tok).code; got != http.StatusUnauthorized {
			t.Errorf("%s session still works after a replay: status %d, want 401", name, got)
		}
	}
}

// TestRevokedSessionIsRejected covers the plain revoke path: a session that was
// withdrawn on purpose must never mint a new token.
func TestRevokedSessionIsRejected(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	pair := e.login("alice", "pw-alice")
	jti := e.jtiOf(pair.RefreshToken)

	store := NewSessionStore(e.db, time.Hour)
	if err := store.Revoke(context.Background(), jti); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	res := e.refresh(pair.RefreshToken)
	if res.code != http.StatusUnauthorized {
		t.Errorf("refresh with a revoked session: status %d, want 401", res.code)
	}
	// A refused session must stay refused on every later attempt, not just the
	// first — otherwise the check was on something other than the row.
	if res := e.refresh(pair.RefreshToken); res.code != http.StatusUnauthorized {
		t.Errorf("second attempt with a revoked session: status %d, want 401", res.code)
	}
}

// TestExpiredSessionRowIsRejected guards the case a token alone cannot catch: a
// row that outlived its token (a late purge, a backdated insert) must not be
// treated as live. A forged token with a future exp but a stale row is exactly
// what the row check is for.
func TestExpiredSessionRowIsRejected(t *testing.T) {
	e := newEnv(t)
	userID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	pair := e.login("alice", "pw-alice")
	jti := e.jtiOf(pair.RefreshToken)

	if _, err := e.db.Exec(`UPDATE auth_sessions SET expires_at = ? WHERE jti = ?`,
		time.Now().UTC().Add(-time.Hour), jti); err != nil {
		t.Fatalf("backdate session row: %v", err)
	}

	if res := e.refresh(pair.RefreshToken).code; res != http.StatusUnauthorized {
		t.Errorf("refresh against an expired session row: status %d, want 401", res)
	}
	if n := e.liveSessionCount(userID); n != 0 {
		t.Errorf("an expired row is still live: %d", n)
	}
}

// TestUnknownJTIRevokesEverySession covers the token that verifies but was
// never issued here: a forged payload, or a row that was purged. There is no way
// to tell it from a theft, so it gets the same treatment.
func TestUnknownJTIRevokesEverySession(t *testing.T) {
	e := newEnv(t)
	userID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	live := e.login("alice", "pw-alice")
	orphan := e.login("alice", "pw-alice")
	orphanJTI := e.jtiOf(orphan.RefreshToken)

	// Remove the row while leaving the signed token intact — the shape a token
	// recovered from a log takes once its row is gone.
	if _, err := e.db.Exec(`DELETE FROM auth_sessions WHERE jti = ?`, orphanJTI); err != nil {
		t.Fatalf("delete session row: %v", err)
	}

	if got := e.refresh(orphan.RefreshToken).code; got != http.StatusUnauthorized {
		t.Errorf("refresh with an unknown jti: status %d, want 401", got)
	}
	if n := e.liveSessionCount(userID); n != 0 {
		t.Errorf("unknown jti left %d session(s) live; the user must be logged out everywhere", n)
	}
	if got := e.refresh(live.RefreshToken).code; got != http.StatusUnauthorized {
		t.Errorf("the innocent session survived an unknown-jti rejection: status %d, want 401", got)
	}
}

// TestLogoutRevokes proves POST /api/auth/logout withdraws the presented
// session, which is the only credential that survives a browser restart.
func TestLogoutRevokes(t *testing.T) {
	e := newEnv(t)
	userID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	pair := e.login("alice", "pw-alice")
	other := e.login("alice", "pw-alice")

	res := e.do(http.MethodPost, "/api/auth/logout", pair.AccessToken,
		map[string]string{"refresh_token": pair.RefreshToken})
	if res.code != http.StatusOK {
		t.Fatalf("logout: status %d, body %s", res.code, res.body)
	}

	if e.sessionRow(e.jtiOf(pair.RefreshToken)).Revoked == false {
		t.Error("logout left the session row live")
	}
	// Assert the surviving session is intact by the count, not by presenting its
	// refresh token. A revoked token is a replay, and the refresh path treats a
	// replay as a broadcast revoke — so probing with one would log the other
	// browser out and the test would be measuring its own side effect.
	if n := e.liveSessionCount(userID); n != 1 {
		t.Errorf("expected 1 surviving session, got %d", n)
	}
	// It is genuinely still usable, on a fresh token rather than the probe.
	if got := e.refreshOK(other.RefreshToken); got.AccessToken == "" {
		t.Error("the surviving session returned no access token")
	}
}

func TestLogoutRefusesAnotherUsersRefreshToken(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	e.seedUser("mallory", "pw-mallory", rbac.RoleAdmin)
	victim := e.login("alice", "pw-alice")
	malloryTok := e.login("mallory", "pw-mallory")

	// Mallory presents her access token with Alice's refresh token. Honouring it
	// would turn "log me out" into "log everyone out", and the audit trail with it.
	res := e.do(http.MethodPost, "/api/auth/logout", malloryTok.AccessToken,
		map[string]string{"refresh_token": victim.RefreshToken})
	if res.code != http.StatusForbidden {
		t.Errorf("logout with someone else's refresh token: status %d, want 403", res.code)
	}
	if got := e.refresh(victim.RefreshToken).code; got != http.StatusOK {
		t.Errorf("the victim's session was revoked by another user: status %d, want 200", got)
	}
}

// TestDisabledUserCannotRefresh ties session lifetime to the user record. A
// deactivated account must lose its ability to renew even though nothing about
// its signed token changed.
func TestDisabledUserCannotRefresh(t *testing.T) {
	e := newEnv(t)
	userID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	pair := e.login("alice", "pw-alice")
	other := e.login("alice", "pw-alice")

	if _, err := e.db.Exec(`UPDATE users SET is_active = 0 WHERE id = ?`, userID); err != nil {
		t.Fatalf("deactivate user: %v", err)
	}

	if got := e.refresh(pair.RefreshToken).code; got != http.StatusUnauthorized {
		t.Errorf("disabled user refreshing: status %d, want 401", got)
	}
	if n := e.liveSessionCount(userID); n != 0 {
		t.Errorf("a disabled user kept %d live session(s)", n)
	}
	if got := e.refresh(other.RefreshToken).code; got != http.StatusUnauthorized {
		t.Errorf("second session of a disabled user still works: status %d, want 401", got)
	}
	// A disabled user must also be unable to start a new session.
	if got := e.do(http.MethodPost, "/api/auth/login", "",
		map[string]string{"username": "alice", "password": "pw-alice"}).code; got != http.StatusUnauthorized {
		t.Errorf("disabled user logging in: status %d, want 401", got)
	}
}

// TestRoleChangeForcesRelogin checks the role claim against the live users row
// rather than trusting the token. Without it, a token minted at viewer level
// silently gains admin on promotion, and a demoted admin keeps minting admin
// refreshes until it expires.
func TestRoleChangeForcesRelogin(t *testing.T) {
	e := newEnv(t)
	userID := e.seedUser("alice", "pw-alice", rbac.RoleViewer)

	old := e.login("alice", "pw-alice")
	if got := e.refresh(old.RefreshToken).code; got != http.StatusOK {
		t.Fatalf("refresh before the role change: status %d, want 200", got)
	}

	// Promoted. The token in hand still says viewer.
	if _, err := e.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, rbac.RoleAdmin, userID); err != nil {
		t.Fatalf("promote user: %v", err)
	}
	if got := e.refresh(old.RefreshToken).code; got != http.StatusUnauthorized {
		t.Errorf("viewer token refreshed after promotion: status %d, want 401", got)
	}
	if n := e.liveSessionCount(userID); n != 0 {
		t.Errorf("role change left %d live session(s)", n)
	}

	// A fresh login picks up the new role.
	fresh := e.login("alice", "pw-alice")
	rotated := e.refreshOK(fresh.RefreshToken)
	claims, err := e.jwt.ParseKind(rotated.RefreshToken, KindRefresh)
	if err != nil {
		t.Fatalf("parse rotated token: %v", err)
	}
	if claims.Role != rbac.RoleAdmin {
		t.Errorf("role after re-login = %q, want %q", claims.Role, rbac.RoleAdmin)
	}
}

// TestDemotedAdminLosesAdminRefreshes is the mirror image of the promotion case,
// and the one that matters: a stale admin token must not keep minting admin
// credentials after the demotion.
func TestDemotedAdminLosesAdminRefreshes(t *testing.T) {
	e := newEnv(t)
	userID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	pair := e.login("alice", "pw-alice")

	if _, err := e.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, rbac.RoleViewer, userID); err != nil {
		t.Fatalf("demote user: %v", err)
	}

	if got := e.refresh(pair.RefreshToken).code; got != http.StatusUnauthorized {
		t.Errorf("demoted admin refreshed: status %d, want 401", got)
	}

	fresh := e.login("alice", "pw-alice")
	viewerAccess, err := e.jwt.Parse(fresh.AccessToken)
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	if viewerAccess.Role != rbac.RoleViewer {
		t.Errorf("role after re-login = %q, want viewer", viewerAccess.Role)
	}
}

// TestRefreshTokenRejectedAsBearer is the middleware guarantee: a week-long
// credential that no logout touches must never work as an API credential.
func TestRefreshTokenRejectedAsBearer(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	pair := e.login("alice", "pw-alice")

	if got := e.do(http.MethodGet, "/api/whoami", pair.RefreshToken, nil).code; got != http.StatusUnauthorized {
		t.Errorf("refresh token as Bearer on an API route: status %d, want 401", got)
	}
	// The access token from the same pair still works, so the refusal above is
	// about the kind and not about the signature.
	if got := e.do(http.MethodGet, "/api/whoami", pair.AccessToken, nil).code; got != http.StatusOK {
		t.Errorf("access token as Bearer: status %d, want 200", got)
	}
	// The query-parameter path no longer exists, so a refresh token cannot
	// reach an API route that way at all. This is the regression test for that
	// removal: an access token in the query string is refused too, which is
	// what makes the removal safe rather than merely a change of channel. If
	// the fallback ever comes back, this is the test that says why it should
	// not.
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+"/api/whoami?token="+pair.AccessToken, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("query-token request: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("access token via ?token=: status %d, want 401; the query path must not accept any token", res.StatusCode)
	}
}

// TestAccessTokenRejectedAtRefresh is the other half of the same separation: an
// access token must not be redeemable for a new pair, or every 15-minute token
// becomes a perpetual session credential and the session table is bypassed.
func TestAccessTokenRejectedAtRefresh(t *testing.T) {
	e := newEnv(t)
	userID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	pair := e.login("alice", "pw-alice")

	before := e.liveSessionCount(userID)
	if got := e.refresh(pair.AccessToken).code; got != http.StatusUnauthorized {
		t.Errorf("access token at the refresh endpoint: status %d, want 401", got)
	}
	if after := e.liveSessionCount(userID); after != before {
		t.Errorf("a refused access token created a session (%d -> %d)", before, after)
	}
	// The refusal must not be a broadcast logout: a bad credential is not proof
	// of theft, it is proof of a bug or a stale client.
	if got := e.refresh(pair.RefreshToken).code; got != http.StatusOK {
		t.Errorf("the real refresh token stopped working: status %d, want 200", got)
	}
}

// TestRevokeAnotherUserSessionIsRefused covers DELETE /api/auth/sessions/{jti}.
// The check has to be on the row, and the refusal must not confirm that the id
// exists — otherwise the endpoint is an enumeration oracle.
func TestRevokeAnotherUserSessionIsRefused(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	e.seedUser("bob", "pw-bob", rbac.RoleAdmin)

	alice := e.login("alice", "pw-alice")
	bob := e.login("bob", "pw-bob")
	bobJTI := e.jtiOf(bob.RefreshToken)

	res := e.do(http.MethodDelete, "/api/auth/sessions/"+bobJTI, alice.AccessToken, nil)
	if res.code != http.StatusNotFound {
		t.Errorf("revoking another user's session: status %d, want 404", res.code)
	}
	if got := e.refresh(bob.RefreshToken).code; got != http.StatusOK {
		t.Errorf("bob's session was revoked by alice: status %d, want 200", got)
	}

	// An id that does not exist must answer identically, so a caller cannot
	// probe for real session ids.
	missing := e.do(http.MethodDelete, "/api/auth/sessions/does-not-exist", alice.AccessToken, nil)
	if missing.code != res.code || string(missing.body) != string(res.body) {
		t.Errorf("a nonexistent jti answered %d %s but another user's jti answered %d %s — the response leaks existence",
			missing.code, missing.body, res.code, res.body)
	}
}

// TestRevokeOwnSession covers the intended use of the same endpoint, and checks
// it actually withdraws rather than just answering 200.
func TestRevokeOwnSession(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	pair := e.login("alice", "pw-alice")
	other := e.login("alice", "pw-alice")
	jti := e.jtiOf(pair.RefreshToken)
	userID := e.sessionRow(jti).UserID

	res := e.do(http.MethodDelete, "/api/auth/sessions/"+jti, other.AccessToken, nil)
	if res.code != http.StatusOK {
		t.Fatalf("revoke own session: status %d, body %s", res.code, res.body)
	}
	if !e.sessionRow(jti).Revoked {
		t.Error("the session row is still live after a 200")
	}
	// Counted rather than probed: see TestLogoutRevokes. Presenting a revoked
	// token is a replay, and the replay path revokes every session for the user,
	// which would make this test report the very failure it is looking for.
	if n := e.liveSessionCount(userID); n != 1 {
		t.Errorf("%d sessions live, want 1: revoking one withdrew another", n)
	}
}

// TestListSessionsOnlyShowsCallersOwn proves the listing is scoped. A session
// list that leaked every user would hand an attacker a target list of live
// refresh-token ids.
func TestListSessionsOnlyShowsCallersOwn(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	e.seedUser("bob", "pw-bob", rbac.RoleViewer)

	aliceA := e.login("alice", "pw-alice")
	aliceB := e.login("alice", "pw-alice")
	e.login("bob", "pw-bob")

	res := e.do(http.MethodGet, "/api/auth/sessions", aliceA.AccessToken, nil)
	if res.code != http.StatusOK {
		t.Fatalf("list sessions: status %d, body %s", res.code, res.body)
	}
	var rows []Session
	if err := json.Unmarshal(res.body, &rows); err != nil {
		t.Fatalf("decode session list: %v (body=%s)", err, res.body)
	}
	if len(rows) != 2 {
		t.Fatalf("expected alice's 2 sessions, got %d (body=%s)", len(rows), res.body)
	}
	for _, row := range rows {
		if row.Role != "" {
			t.Errorf("session list exposes role %q, which may no longer be the user's", row.Role)
		}
		// The list is scoped to the caller, so user_id on the wire is both
		// redundant and a leak if the query is ever widened.
		if strings.Contains(row.JTI, "user-") {
			t.Errorf("jti %q looks like a user id", row.JTI)
		}
	}
	// A revoked session must not appear, or the console would offer to revoke it
	// again and report a session that does not exist.
	if err := NewSessionStore(e.db, time.Hour).Revoke(context.Background(), e.jtiOf(aliceB.RefreshToken)); err != nil {
		t.Fatalf("revoke for list check: %v", err)
	}
	res = e.do(http.MethodGet, "/api/auth/sessions", aliceA.AccessToken, nil)
	if err := json.Unmarshal(res.body, &rows); err != nil {
		t.Fatalf("decode session list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("after revoking one, list returned %d sessions, want 1", len(rows))
	}
}

func TestListSessionsReturnsEmptyArrayNotNull(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	pair := e.login("alice", "pw-alice")
	if err := NewSessionStore(e.db, time.Hour).Revoke(context.Background(), e.jtiOf(pair.RefreshToken)); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	res := e.do(http.MethodGet, "/api/auth/sessions", pair.AccessToken, nil)
	if res.code != http.StatusOK {
		t.Fatalf("list sessions: status %d, body %s", res.code, res.body)
	}
	if got := string(bytes.TrimSpace(res.body)); got != "[]" {
		t.Errorf("empty session list = %q; the console maps over this and null blanks the page", got)
	}
}

// TestRotateIsAtomicUnderConcurrency is the one that cannot be checked by
// reading the code. N callers race to rotate the same jti; SQLite serialises
// them, and the transaction's guarded UPDATE must let exactly one through. Two
// winners would mean two live sessions from one token — the replay window the
// whole design exists to close.
func TestRotateIsAtomicUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	userID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	store := NewSessionStore(e.db, time.Hour)

	pair := e.login("alice", "pw-alice")
	oldJTI := e.jtiOf(pair.RefreshToken)

	const racers = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, racers)
	newJTIs := make([]string, racers)

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			jti, err := newJTI()
			if err != nil {
				results[i] = err
				return
			}
			newJTIs[i] = jti
			results[i] = store.Rotate(context.Background(), oldJTI, jti)
		}(i)
	}
	close(start)
	wg.Wait()

	var winners []int
	var rejected int
	for i, err := range results {
		switch {
		case err == nil:
			winners = append(winners, i)
		case errors.Is(err, ErrSessionNotUsable):
			rejected++
		default:
			t.Errorf("racer %d: unexpected error %v", i, err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("got %d winners rotating one jti, want exactly 1 (%d were rejected with %v)",
			len(winners), rejected, ErrSessionNotUsable)
	}
	if rejected != racers-1 {
		t.Errorf("%d racers were rejected, want %d", rejected, racers-1)
	}

	// One live row for the user, not two. A second one would be a session the
	// user never asked for and cannot see.
	if n := e.liveSessionCount(userID); n != 1 {
		t.Errorf("after the race %d sessions are live, want 1", n)
	}
	// And the loser's jti was never inserted, so a rollback left no residue.
	for i, jti := range newJTIs {
		if i == winners[0] {
			continue
		}
		if _, err := store.Get(context.Background(), jti); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("loser %d's jti exists after rollback (err=%v)", i, err)
		}
	}
}

// TestRotateRefusesRevokedAndExpired covers the two inputs a rotation must not
// accept, independently of the HTTP path.
func TestRotateRefusesRevokedAndExpired(t *testing.T) {
	e := newEnv(t)
	store := NewSessionStore(e.db, time.Hour)
	ctx := context.Background()

	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	pair := e.login("alice", "pw-alice")
	jti := e.jtiOf(pair.RefreshToken)

	if err := store.Revoke(ctx, jti); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	replacement, err := newJTI()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Rotate(ctx, jti, replacement); !errors.Is(err, ErrSessionNotUsable) {
		t.Errorf("rotating a revoked session: err = %v, want %v", err, ErrSessionNotUsable)
	}
	// The failed rotation must not have left the replacement behind.
	if _, err := store.Get(ctx, replacement); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("a refused rotation still created a session row (err=%v)", err)
	}

	expired := e.login("alice", "pw-alice")
	expiredJTI := e.jtiOf(expired.RefreshToken)
	if _, err := e.db.Exec(`UPDATE auth_sessions SET expires_at = ? WHERE jti = ?`,
		time.Now().UTC().Add(-time.Hour), expiredJTI); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if err := store.Rotate(ctx, expiredJTI, replacement); !errors.Is(err, ErrSessionNotUsable) {
		t.Errorf("rotating an expired session: err = %v, want %v", err, ErrSessionNotUsable)
	}
	if _, err := store.Get(ctx, replacement); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("rotating an expired session still created a row (err=%v)", err)
	}
}

// TestPurgeExpiredKeepsRevokedRows states the rule PurgeExpired has to follow.
// Deleting revoked rows would make their tokens look unissued again, which is
// the one thing revocation exists to prevent.
func TestPurgeExpiredKeepsRevokedRows(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	store := NewSessionStore(e.db, time.Hour)
	ctx := context.Background()

	revoked := e.login("alice", "pw-alice")
	live := e.login("alice", "pw-alice")
	expired := e.login("alice", "pw-alice")
	expiredJTI := e.jtiOf(expired.RefreshToken)

	if err := store.Revoke(ctx, e.jtiOf(revoked.RefreshToken)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := e.db.Exec(`UPDATE auth_sessions SET expires_at = ? WHERE jti = ?`,
		time.Now().UTC().Add(-time.Hour), expiredJTI); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if err := store.PurgeExpired(ctx); err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}

	if _, err := store.Get(ctx, expiredJTI); !errors.Is(err, ErrSessionNotFound) {
		t.Error("an expired row survived the purge")
	}
	if _, err := store.Get(ctx, e.jtiOf(revoked.RefreshToken)); err != nil {
		t.Errorf("a revoked row was purged; its token would look unissued again: %v", err)
	}
	if got := e.refresh(revoked.RefreshToken).code; got != http.StatusUnauthorized {
		t.Errorf("a purged-and-revoked token became usable: status %d, want 401", got)
	}
	if _, err := store.Get(ctx, e.jtiOf(live.RefreshToken)); err != nil {
		t.Errorf("a live row was purged: %v", err)
	}
}

// TestRevokeAllForUserIsScoped proves logout-everywhere does not reach past one
// account. This is the operation that fires on a replay, so over-reach here
// would be a denial-of-service any attacker could trigger.
func TestRevokeAllForUserIsScoped(t *testing.T) {
	e := newEnv(t)
	aliceID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	bobID := e.seedUser("bob", "pw-bob", rbac.RoleAdmin)

	e.login("alice", "pw-alice")
	e.login("alice", "pw-alice")
	bob := e.login("bob", "pw-bob")

	if err := NewSessionStore(e.db, time.Hour).RevokeAllForUser(context.Background(), aliceID); err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}
	if n := e.liveSessionCount(aliceID); n != 0 {
		t.Errorf("alice kept %d live session(s)", n)
	}
	if n := e.liveSessionCount(bobID); n != 1 {
		t.Errorf("bob's sessions were affected: %d live, want 1", n)
	}
	if got := e.refresh(bob.RefreshToken).code; got != http.StatusOK {
		t.Errorf("bob's refresh: status %d, want 200", got)
	}
}

// TestLegacyTokenWithoutKindIsAccepted is the backward-compatibility contract:
// a token minted before kinds existed must keep working, so a deploy does not
// sign out every user mid-session. It is only ever accepted as an access token,
// and only while it is fresh: the ambiguity window is bounded in
// middleware.go, because the pre-change REFRESH token also carried no kind and
// was minted with the 7-day refresh TTL.
func TestLegacyTokenWithoutKindIsAccepted(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	legacy := signLegacyAccess(t, testSecret, "user-alice", "alice", rbac.RoleAdmin)

	if got := e.do(http.MethodGet, "/api/whoami", legacy, nil).code; got != http.StatusOK {
		t.Errorf("pre-change access token: status %d, want 200", got)
	}
	// It must not be redeemable at the refresh endpoint, which is where an
	// unkinded token would otherwise buy a week of access.
	if got := e.refresh(legacy).code; got != http.StatusUnauthorized {
		t.Errorf("pre-change token at the refresh endpoint: status %d, want 401", got)
	}
}

// TestSessionRoutesRequireAuth proves the new endpoints are not anonymous. A
// sessions list with no credential, or a logout, is a free oracle.
func TestSessionRoutesRequireAuth(t *testing.T) {
	e := newEnv(t)
	for _, spec := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/auth/logout", map[string]string{"refresh_token": "anything"}},
		{http.MethodGet, "/api/auth/sessions", nil},
		{http.MethodDelete, "/api/auth/sessions/whatever", nil},
	} {
		res := e.do(spec.method, spec.path, "", spec.body)
		if res.code != http.StatusUnauthorized {
			t.Errorf("%s %s with no credential: status %d, want 401", spec.method, spec.path, res.code)
		}
	}
}

// TestRegisterOnServeMux exercises the other half of Register's type switch.
// Production runs chi, so the ServeMux branch is what every other mux on the
// server would get, and the {jti} wildcard is the part most likely to differ
// between the two: chi and ServeMux do not agree on where a path parameter is
// published, so a branch that registers fine and 500s on delete is exactly the
// kind of thing that only shows up when someone switches routers.
func TestRegisterOnServeMux(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "mux.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	jwtSvc := NewJWTService(testSecret, 15*time.Minute, time.Hour)
	mux := http.NewServeMux()
	h := NewLoginHandler(d, jwtSvc)
	t.Cleanup(h.Close)
	h.Register(mux)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	e := &env{t: t, db: d, jwt: jwtSvc, srv: ts}
	userID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	first := e.login("alice", "pw-alice")
	second := e.login("alice", "pw-alice")
	firstJTI := e.jtiOf(first.RefreshToken)

	// The collection route must not be shadowed by the {jti} wildcard. If
	// ServeMux matched "/api/auth/sessions" as a jti, this would not be a list.
	res := e.do(http.MethodGet, "/api/auth/sessions", first.AccessToken, nil)
	if res.code != http.StatusOK {
		t.Fatalf("GET sessions on ServeMux: status %d, body %s", res.code, res.body)
	}
	var rows []Session
	if err := json.Unmarshal(res.body, &rows); err != nil {
		t.Fatalf("decode session list: %v (body=%s)", err, res.body)
	}
	if len(rows) != 2 {
		t.Errorf("ServeMux listed %d sessions, want 2", len(rows))
	}

	// The wildcard must reach the handler, which is where the two routers differ.
	if got := e.do(http.MethodDelete, "/api/auth/sessions/"+firstJTI, second.AccessToken, nil).code; got != http.StatusOK {
		t.Errorf("DELETE on ServeMux: status %d, want 200 — the {jti} wildcard did not reach the handler", got)
	}
	if !e.sessionRow(firstJTI).Revoked {
		t.Error("DELETE on ServeMux reported success without revoking the session")
	}
	if n := e.liveSessionCount(userID); n != 1 {
		t.Errorf("%d sessions live after the delete, want 1", n)
	}
}

// TestLogoutWithOnlyTheAccessTokenRevokes covers the presentation the console
// actually uses: an Authorization header carrying the ACCESS token and no body.
//
// This regressed silently. That path was read as a refresh token, ParseKind
// refused it, and logout answered 401 — but AuthContext swallows a failed
// logout on purpose and clears the tokens anyway, so the user saw a clean
// sign-out while the refresh token stayed live on disk for its full TTL.
func TestLogoutWithOnlyTheAccessTokenRevokes(t *testing.T) {
	e := newEnv(t)
	userID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	pair := e.login("alice", "pw-alice")

	jti := e.jtiOf(pair.RefreshToken)

	res := e.do(http.MethodPost, "/api/auth/logout", pair.AccessToken, nil)
	if res.code != http.StatusOK {
		t.Fatalf("logout with only the access token: status %d, body %s", res.code, res.body)
	}
	if !e.sessionRow(jti).Revoked {
		t.Error("logout answered 200 but left the session row live")
	}
	if n := e.liveSessionCount(userID); n != 0 {
		t.Errorf("%d live session(s) after logout, want 0 — the refresh token is still usable", n)
	}
	if got := e.refresh(pair.RefreshToken).code; got != http.StatusUnauthorized {
		t.Errorf("the withdrawn session still refreshes: status %d, want 401", got)
	}
}

// A token is allowed to name a session it does not own only so that the
// cross-user refusal stays a 403 rather than turning into a 404 that would leak
// whether the id exists. What it must never do is actually revoke.
func TestLogoutAccessTokenCannotRevokeAnotherSession(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	e.seedUser("mallory", "pw-mallory", rbac.RoleTechnician)
	victim := e.login("alice", "pw-alice")
	malloryPair := e.login("mallory", "pw-mallory")
	victimJTI := e.jtiOf(victim.RefreshToken)

	// Mallory rewrites the sid on her own access token. The signature check
	// fails, so this is refused outright — the point is that it cannot succeed.
	forged := strings.Replace(malloryPair.AccessToken, ".e", ".f", 1)
	res := e.do(http.MethodPost, "/api/auth/logout", forged, nil)
	if res.code == http.StatusOK {
		t.Errorf("a tampered access token revoked a session: status %d, body %s", res.code, res.body)
	}
	if e.sessionRow(victimJTI).Revoked {
		t.Error("the victim's session was revoked by another user's logout")
	}
}

// The sid claim is a session identifier, not an authority. Two sessions of the
// SAME user must still be individually revocable, which is only true because
// RevokeForUser scopes the write to a single jti rather than to the user.
func TestLogoutRevokesOnlyTheSessionTheTokenNames(t *testing.T) {
	e := newEnv(t)
	userID := e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	phone := e.login("alice", "pw-alice")
	laptop := e.login("alice", "pw-alice")

	if res := e.do(http.MethodPost, "/api/auth/logout", phone.AccessToken, nil); res.code != http.StatusOK {
		t.Fatalf("logout: status %d, body %s", res.code, res.body)
	}
	if n := e.liveSessionCount(userID); n != 1 {
		t.Errorf("%d live session(s) after signing out one browser, want 1", n)
	}
	if !e.sessionRow(e.jtiOf(laptop.RefreshToken)).Revoked == false {
		t.Error("signing out the phone also revoked the laptop")
	}
}

// A refresh rotates the session id. The access token handed out alongside must
// carry the NEW id, or the console's next logout would revoke a row that the
// rotation already retired — silently succeeding while the live session stayed
// usable.
func TestRefreshedAccessTokenCarriesTheSuccessorSession(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)
	first := e.login("alice", "pw-alice")
	firstJTI := e.jtiOf(first.RefreshToken)

	next := e.refreshOK(first.RefreshToken)
	nextJTI := e.jtiOf(next.RefreshToken)
	if nextJTI == firstJTI {
		t.Fatal("rotation did not change the session id")
	}

	claims, err := e.jwt.ParseKind(next.AccessToken, KindAccess)
	if err != nil {
		t.Fatalf("parse refreshed access token: %v", err)
	}
	if claims.SessionID != nextJTI {
		t.Errorf("refreshed access token carries sid %q, want %q", claims.SessionID, nextJTI)
	}

	// And the end-to-end consequence: logging out with it must retire the row
	// that the refresh actually created.
	if res := e.do(http.MethodPost, "/api/auth/logout", next.AccessToken, nil); res.code != http.StatusOK {
		t.Fatalf("logout after refresh: status %d, body %s", res.code, res.body)
	}
	if !e.sessionRow(nextJTI).Revoked {
		t.Error("logout after refresh left the successor session live")
	}
}

// TestKindlessTokenIsRefusedAsBearerOnceItIsOld is the hole kinds were added to
// close, checked from the other side.
//
// Before kinds, Issue() signed the refresh token with the REFRESH TTL and
// neither token carried anything to tell them apart. A user who logged in a week
// before the change therefore still held a week-old token that parseAccess
// would accept as a Bearer for its whole remaining life. The legacy acceptance
// window is therefore bounded here, so a kindless token is usable only while it
// is fresh enough that it plausibly IS a short-lived access token.
func TestKindlessTokenIsRefusedAsBearerOnceItIsOld(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	sign := func(iat time.Time) string {
		t.Helper()
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
			UserID:   "alice-1",
			Username: "alice",
			Role:     rbac.RoleAdmin,
			RegisteredClaims: jwt.RegisteredClaims{
				IssuedAt:  jwt.NewNumericDate(iat),
				ExpiresAt: jwt.NewNumericDate(iat.Add(24 * 7 * time.Hour)),
				Subject:   "alice-1",
			},
		})
		signed, err := tok.SignedString([]byte(testSecret))
		if err != nil {
			t.Fatalf("sign kindless token: %v", err)
		}
		return signed
	}

	fresh := e.do(http.MethodGet, "/api/whoami", sign(time.Now().Add(-time.Minute)), nil)
	if fresh.code != http.StatusOK {
		t.Errorf("a kindless token minted a minute ago was refused: status %d, body %s. The compat window is meant to be 15 minutes, not zero", fresh.code, fresh.body)
	}

	// The shape that actually matters: the pre-change REFRESH token, a week old
	// and still carrying a week of validity.
	stale := e.do(http.MethodGet, "/api/whoami", sign(time.Now().Add(-48*time.Hour)), nil)
	if stale.code != http.StatusUnauthorized {
		t.Errorf("a 48h-old kindless token authenticated: status %d, want 401. A week-old refresh token is accepted as a Bearer", stale.code)
	}
}

// The ?token= fallback is gone, so there is no second channel for a kindless
// token to slip through. What is left to prove is that the one remaining
// channel refuses it, which is what parseAccess is for -- the check does not
// depend on which transport carried the token.
func TestKindlessTokenIsRefusedEverywhere(t *testing.T) {
	e := newEnv(t)
	e.seedUser("alice", "pw-alice", rbac.RoleAdmin)

	old := time.Now().Add(-48 * time.Hour)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		UserID:   "alice-1",
		Username: "alice",
		Role:     rbac.RoleAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(old),
			ExpiresAt: jwt.NewNumericDate(old.Add(24 * 7 * time.Hour)),
			Subject:   "alice-1",
		},
	})
	signed, err := tok.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign kindless token: %v", err)
	}
	// As a Bearer, the only channel that remains.
	if res := e.do(http.MethodGet, "/api/whoami", signed, nil); res.code != http.StatusUnauthorized {
		t.Errorf("a 48h-old kindless token authenticated as a Bearer: status %d, want 401", res.code)
	}
	// And in the query string, which no code path reads anymore. Refused for
	// the absence of a header rather than for the token's age, which is the
	// stronger property: nothing about the value matters because nothing
	// inspects it.
	if res := e.do(http.MethodGet, "/api/whoami?token="+signed, "", nil); res.code != http.StatusUnauthorized {
		t.Errorf("a kindless token authenticated via ?token=: status %d, want 401", res.code)
	}
}

package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/httpguard"
)

// chiRouter is the subset of *chi.Mux used by Register. Declaring it as an
// interface lets Register accept both chi and net/http muxes without forcing a
// hard dependency on a single router type at this layer.
type chiRouter interface {
	Post(pattern string, handlerFn http.HandlerFunc)
	Get(pattern string, handlerFn http.HandlerFunc)
	Delete(pattern string, handlerFn http.HandlerFunc)
}

// LoginHandler issues JWTs for admin console users.
type LoginHandler struct {
	db       *sqlx.DB
	jwt      *JWTService
	sessions *SessionStore
	limiter  *IPRateLimiter
}

func NewLoginHandler(db *sqlx.DB, jwt *JWTService) *LoginHandler {
	return &LoginHandler{
		db:       db,
		jwt:      jwt,
		sessions: NewSessionStore(db, jwt.refreshTokenTTL),
		limiter:  NewIPRateLimiter(5, 1*time.Minute, 5*time.Minute),
	}
}

// WithRateLimiter configures a custom rate limiter (e.g. for unit tests).
func (h *LoginHandler) WithRateLimiter(l *IPRateLimiter) *LoginHandler {
	if h.limiter != nil {
		h.limiter.Close()
	}
	h.limiter = l
	return h
}

// WithSessionStore replaces the session store, keeping its lifetime bound to
// the token TTL the store was built with.
func (h *LoginHandler) WithSessionStore(s *SessionStore) *LoginHandler {
	h.sessions = s
	return h
}

// Close gracefully stops the background rate limiter cleanup.
func (h *LoginHandler) Close() {
	if h.limiter != nil {
		h.limiter.Close()
	}
}

// guarded wraps a handler in RequireAuth. Register takes plain HandlerFuncs on
// both router types, so the middleware has to be applied here rather than at
// the call site -- and applying it in Register is what keeps every authenticated
// session route from being reachable because one line forgot the wrapper.
func (h *LoginHandler) guarded(next http.HandlerFunc) http.HandlerFunc {
	return h.jwt.RequireAuth(next).ServeHTTP
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Register mounts the auth endpoints. Accepts any standard mux that supports
// the Go 1.22+ "METHOD /path" pattern (http.ServeMux) or chi routes.
//
// The session routes are registered on a router that already has a
// "METHOD /path/{jti}" pattern, so ServeMux can take the wildcard segment
// without the pattern colliding with "GET /api/auth/sessions".
func (h *LoginHandler) Register(mux any) {
	// login and refresh are reachable without credentials and decode the body
	// before they check anything, so they carry the same 1 MiB cap as
	// enrollment. logout is behind RequireAuth already and reads no body.
	limited := httpguard.LimitJSONBodyFunc
	switch m := mux.(type) {
	case *http.ServeMux:
		m.HandleFunc("POST /api/auth/login", limited(h.login))
		m.HandleFunc("POST /api/auth/refresh", limited(h.refresh))
		m.Handle("POST /api/auth/logout", h.jwt.RequireAuth(http.HandlerFunc(h.logout)))
		m.Handle("GET /api/auth/sessions", h.jwt.RequireAuth(http.HandlerFunc(h.listSessions)))
		m.Handle("DELETE /api/auth/sessions/{jti}", h.jwt.RequireAuth(http.HandlerFunc(h.revokeSession)))
		m.Handle("POST /api/auth/ws-ticket", h.jwt.RequireAuth(http.HandlerFunc(h.issueWSTicket)))

	case chiRouter:
		m.Post("/api/auth/login", limited(h.login))
		m.Post("/api/auth/refresh", limited(h.refresh))
		m.Post("/api/auth/logout", h.guarded(h.logout))
		m.Get("/api/auth/sessions", h.guarded(h.listSessions))
		m.Delete("/api/auth/sessions/{jti}", h.guarded(h.revokeSession))
		m.Post("/api/auth/ws-ticket", h.guarded(h.issueWSTicket))
	default:
		panic("auth.LoginHandler.Register: unsupported mux type")
	}
}

func (h *LoginHandler) login(w http.ResponseWriter, r *http.Request) {
	clientIP := ClientIP(r)
	if allowed, wait := h.limiter.IsAllowed(clientIP); !allowed {
		waitSec := int(wait.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(waitSec))
		writeErr(w, http.StatusTooManyRequests, fmt.Sprintf("too many failed login attempts, please try again in %d seconds", waitSec))
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge,
				"request body exceeds the 1 MiB limit for login")
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password required")
		return
	}

	var u struct {
		ID           string `db:"id"`
		Username     string `db:"username"`
		PasswordHash string `db:"password_hash"`
		Role         string `db:"role"`
		// is_active is what user-management writes when an admin deactivates
		// someone. The password_hash check below catches a different condition --
		// a user row with no usable credential -- and login is the one place
		// that already has the hash in hand to check it.
		IsActive int `db:"is_active"`
	}
	err := h.db.GetContext(r.Context(), &u, `
		SELECT u.id, u.username, u.password_hash, u.role,
		       COALESCE(u.is_active, 1) AS is_active
		FROM users u WHERE u.username = ?`, req.Username)
	if err != nil {
		// Do not leak whether the username exists.
		h.limiter.RecordFailure(clientIP)
		log.Debug().Err(err).Str("username", req.Username).Msg("login unknown user")
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if u.IsActive != 1 || u.PasswordHash == "" {
		h.limiter.RecordFailure(clientIP)
		writeErr(w, http.StatusUnauthorized, "account is disabled")
		return
	}
	if err := ComparePassword(u.PasswordHash, req.Password); err != nil {
		h.limiter.RecordFailure(clientIP)
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// The session id is minted first so the SAME value goes on the auth_sessions
	// row and on both tokens. Deriving it from the refresh token afterwards
	// would leave the access token stamped with nothing, and logout for a client
	// holding only the access token would have no handle to revoke by.
	jti, err := newJTI()
	if err != nil {
		log.Error().Err(err).Msg("mint session id")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	pair, err := h.jwt.IssueForSession(u.ID, u.Username, u.Role, jti)
	if err != nil {
		log.Error().Err(err).Msg("issue jwt")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.sessions.Create(r.Context(), u.ID, u.Username, u.Role, jti,
		r.UserAgent(), ClientIP(r)); err != nil {
		log.Error().Err(err).Msg("create auth session")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	h.limiter.RecordSuccess(clientIP)
	_ = audit.Log(r.Context(), h.db, "user", u.ID, "auth.login", u.ID, nil)
	writeJSON(w, http.StatusOK, pair)
}

func (h *LoginHandler) refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge,
				"request body exceeds the 1 MiB limit for token refresh")
			return
		}
		writeErr(w, http.StatusBadRequest, "refresh_token required")
		return
	}
	claims, err := h.jwt.ParseKind(req.RefreshToken, KindRefresh)
	if err != nil {
		// An access token here would be a caller trying to skip the session
		// record entirely, so it is refused rather than quietly accepted.
		writeErr(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}

	sess, err := h.sessions.Get(r.Context(), claims.ID)
	switch {
	case errors.Is(err, ErrSessionNotFound):
		// No row: either the token was never issued by this server, or its row
		// was purged after expiry, or the signature check passed over a forged
		// payload. Any of those means someone holds a token the user cannot
		// account for.
		h.revokeAll(r.Context(), claims.UserID, "refresh.unknown_jti")
		writeErr(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	case err != nil:
		log.Error().Err(err).Msg("read auth session")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !sess.Live(time.Now().UTC()) {
		// Revoked, expired, or already rotated. Rotated is the interesting case:
		// the token worked once and is being presented a second time, which is
		// the shape a stolen token has.
		h.revokeAll(r.Context(), claims.UserID, "refresh.replay")
		writeErr(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}

	// The role in the token is a snapshot from issue time. A viewer promoted to
	// admin must not be handed an admin session on the strength of the old
	// token, and an admin demoted to viewer must not keep minting admin refreshes
	// until it expires. Either way the answer is a forced re-login, because the
	// next token can only carry a role the server has just re-read.
	live, err := h.readUser(r.Context(), claims.UserID)
	switch {
	case errors.Is(err, errUserGone):
		h.revokeAll(r.Context(), claims.UserID, "refresh.user_disabled")
		writeErr(w, http.StatusUnauthorized, "account is not active")
		return
	case errors.Is(err, sql.ErrNoRows):
		h.revokeAll(r.Context(), claims.UserID, "refresh.user_deleted")
		writeErr(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	case err != nil:
		log.Error().Err(err).Msg("read user for refresh")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if live.Role != claims.Role {
		h.revokeAll(r.Context(), claims.UserID, "refresh.role_changed")
		writeErr(w, http.StatusUnauthorized, "role changed, please sign in again")
		return
	}

	// The successor jti is minted here rather than read back out of the issued
	// refresh token, so the new access token can be stamped with it. The pair is
	// written only after Rotate commits, so a rotation that loses the race never
	// reaches a client.
	nextJTI, err := newJTI()
	if err != nil {
		log.Error().Err(err).Msg("mint successor session id")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.sessions.Rotate(r.Context(), claims.ID, nextJTI); err != nil {
		if errors.Is(err, ErrSessionNotUsable) {
			// Lost the race against a concurrent use of the same token: the other
			// caller has already rotated it, so this one is the replay.
			h.revokeAll(r.Context(), claims.UserID, "refresh.rotate_lost")
		} else {
			log.Error().Err(err).Msg("rotate auth session")
		}
		writeErr(w, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}
	pair, err := h.jwt.IssueForSession(live.ID, live.Username, live.Role, nextJTI)
	if err != nil {
		// The rotation has committed but no token was produced. Revoking the new
		// row keeps the two halves consistent: there is a live session nobody
		// holds a credential for, and leaving it live would mean the next refresh
		// attempt looks like a replay and wipes the user's other sessions too.
		log.Error().Err(err).Msg("issue jwt")
		if rerr := h.sessions.RevokeForUser(r.Context(), nextJTI, live.ID); rerr != nil {
			log.Error().Err(rerr).Msg("revoke orphaned successor session")
		}
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = audit.Log(r.Context(), h.db, "user", live.ID, "auth.refresh", live.ID, nil)
	writeJSON(w, http.StatusOK, pair)
}

// logout revokes the caller's own session.
//
// Three presentations are accepted, and the one that picks the session is not
// always the one that carries the token:
//
//   - a refresh token, from the body or the Authorization header. It names its
//     own session through its jti.
//   - an ACCESS token with no body. It carries no jti -- the jti is the session
//     handle, and only the refresh token is stamped with it -- but it does carry
//     the session id in `sid`.
//
// The access-token path existed and was broken. The console sends
// `Authorization: Bearer <access token>` with an empty body, and that was read
// as a refresh token, so ParseKind refused it, logout answered 401, and the
// session stayed live: the user was told nothing, cleared their tokens locally,
// and the refresh token that was still on disk kept working. The reason to
// accept the access token at all is precisely the case where the client no
// longer has the refresh token, which is also the case with no jti to read.
func (h *LoginHandler) logout(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())

	if raw := h.presentedRefresh(r); raw != "" {
		claims, err := h.jwt.ParseKind(raw, KindRefresh)
		if err != nil {
			// An access token can reach here, from presentedRefresh's header
			// fallback, and it is a legitimate way to say "revoke this session".
			if acc, aerr := h.jwt.ParseKind(raw, KindAccess); aerr == nil && acc.SessionID != "" {
				h.revokeSessionByID(w, r, userID, acc.SessionID)
				return
			}
			writeErr(w, http.StatusUnauthorized, "invalid or expired refresh token")
			return
		}
		if claims.UserID != userID {
			// The access token and the refresh token name different users. Refusing
			// is the only safe reading: honouring it would let anyone holding one
			// credential revoke the other's sessions.
			writeErr(w, http.StatusForbidden, "token does not belong to the authenticated user")
			return
		}
		h.revokeSessionByID(w, r, userID, claims.ID)
		return
	}

	// No body and no header of the caller's own: the session id is the only
	// thing left, and it is on the token RequireAuth already validated.
	if sid := SessionIDFromContext(r.Context()); sid != "" {
		h.revokeSessionByID(w, r, userID, sid)
		return
	}

	writeErr(w, http.StatusBadRequest, "no session to revoke")
}

// revokeSessionByID withdraws one session and writes the response. Ownership is
// enforced in the WHERE clause rather than by a prior read, so a session id
// belonging to another user is indistinguishable from one that does not exist:
// both answer 404, and neither reveals that the id is real.
func (h *LoginHandler) revokeSessionByID(w http.ResponseWriter, r *http.Request, userID, jti string) {
	if jti == "" {
		writeErr(w, http.StatusBadRequest, "no session to revoke")
		return
	}
	if err := h.sessions.RevokeForUser(r.Context(), jti, userID); err != nil && !errors.Is(err, ErrSessionNotFound) {
		log.Error().Err(err).Msg("revoke session on logout")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = audit.Log(r.Context(), h.db, "user", userID, "auth.logout", userID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *LoginHandler) listSessions(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	rows, err := h.sessions.ListActive(r.Context(), userID)
	if err != nil {
		log.Error().Err(err).Msg("list auth sessions")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// issueWSTicket mints a one-time ticket a browser can redeem on a WebSocket
// handshake. The implementation lives in wsticket.go, next to the redeem path
// it shares state with.

func (h *LoginHandler) revokeSession(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	jti := pathJTI(r)
	if jti == "" {
		writeErr(w, http.StatusBadRequest, "session id required")
		return
	}
	// The ownership test is the WHERE clause, not a preceding lookup. Any
	// answer other than "revoked" for someone else's jti would leak which ids
	// exist.
	switch err := h.sessions.RevokeForUser(r.Context(), jti, userID); {
	case err == nil:
		_ = audit.Log(r.Context(), h.db, "user", userID, "auth.session_revoke", jti, nil)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case errors.Is(err, ErrSessionNotFound):
		writeErr(w, http.StatusNotFound, "session not found")
	default:
		log.Error().Err(err).Msg("revoke auth session")
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

// presentedRefresh finds the refresh token on a logged-in request: the JSON
// body if there is one, otherwise the Bearer credential. The Bearer fallback is
// what lets a console log out when its access token has already expired.
func (h *LoginHandler) presentedRefresh(r *http.Request) string {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if req.RefreshToken != "" {
		return req.RefreshToken
	}
	hdr := r.Header.Get("Authorization")
	if parts, ok := strings.CutPrefix(hdr, "Bearer "); ok {
		return strings.TrimSpace(parts)
	}
	return ""
}

// errUserGone is returned by liveUser for a user row that exists but may no
// longer hold a session: deactivated, or stripped of their credential.
var errUserGone = errors.New("user is not active")

// liveUser is the account behind a session: the row re-read at refresh time so
// no token outlives the record it was minted from.
type liveUser struct {
	ID       string `db:"id"`
	Username string `db:"username"`
	Role     string `db:"role"`
}

// readUser re-reads the user row so nothing in a token outlives the record
// behind it. ComparePassword is not involved -- login already verified the
// password, and a reset password must not silently kill every open session.
func (h *LoginHandler) readUser(ctx context.Context, userID string) (*liveUser, error) {
	var u struct {
		liveUser
		PasswordHash string `db:"password_hash"`
		IsActive     int    `db:"is_active"`
	}
	err := h.db.GetContext(ctx, &u, `
		SELECT id, username, role, password_hash, COALESCE(is_active, 1) AS is_active
		FROM users WHERE id = ?`, userID)
	if err != nil {
		return nil, err
	}
	if u.IsActive != 1 || u.PasswordHash == "" {
		return nil, errUserGone
	}
	return &u.liveUser, nil
}

// revokeAll withdraws every session a user holds after a refresh token turned
// out to be unusable, and records why. The reason string is fixed at each call
// site rather than derived from the token, so a log line can never carry token
// contents.
func (h *LoginHandler) revokeAll(ctx context.Context, userID, reason string) {
	if err := h.sessions.RevokeAllForUser(ctx, userID); err != nil {
		// The request is refused either way. Losing the revocation is worth
		// shouting about, though: a replayed token stays live.
		log.Error().Err(err).Str("reason", reason).Msg("revoke all sessions failed")
	}
	log.Warn().Str("reason", reason).Msg("refresh rejected: all sessions for user revoked")
	_ = audit.Log(ctx, h.db, "system", userID, "auth.sessions_revoked", userID,
		map[string]string{"reason": reason})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

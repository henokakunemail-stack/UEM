package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type contextKey string

const (
	// CtxUserID / CtxUsername / CtxSessionID are set on the request context by
	// RequireAuth.
	CtxUserID    contextKey = "uid"
	CtxUsername  contextKey = "usr"
	CtxSessionID contextKey = "sid"
)

// legacyKindTTL bounds how long a token minted before token kinds existed keeps
// working as an API credential.
//
// Such a token has an empty Kind, so it cannot be told apart from an access
// token by anything in it. What it does have is an `exp`, and the important
// fact is which TTL minted it: before this change, Issue() signed the refresh
// token with refreshTokenTTL and the access token with accessTokenTTL, and
// neither carried a kind. So the pre-change refresh token a user has been
// holding for a week is a week-long credential that parseAccess would otherwise
// accept as a Bearer for its remaining lifetime -- the exact hole the kinds
// were added to close, reopened for whoever logged in last.
//
// The bound is applied here, at the edge, and the value is written into the
// comment above this constant's use on purpose: it is a deliberate trade, not a
// default. A user holding such a token is signed out when it expires and signs
// back in, which is the inconvenience of fixing the hole. The alternative --
// accepting empty kinds at any age -- accepts a week of admin access. The
// alternative in the other direction, rejecting empty kinds outright, logs out
// every user with a live token at deploy time for no security gain over
// expiring them, so the cutoff is the shortest one that does not do that.
const legacyKindTTL = 15 * time.Minute

// legacyKindFloor is the timestamp before which a kindless token could not have
// been minted by any version of this code, and is therefore rejected outright
// rather than merely age-checked. It is a compile-time-ish constant rather than
// a config value on purpose: loosening it is a code change, so it cannot happen
// by accident through an env var.
var legacyKindFloor = time.Now().UTC().Add(-legacyKindTTL)

// parseAccess accepts a Bearer credential. A token with an empty Kind was
// minted before kinds existed; it is accepted only while it is still fresh.
//
// Rejecting empty kinds outright would be simpler and would be safe too, but it
// signs out every user with a live token the moment the server restarts, for a
// guarantee no better than the one the age check already gives: both end with
// no kindless token usable, and only one of them is an outage.
func (s *JWTService) parseAccess(rawToken string) (Claims, error) {
	c, err := s.Parse(rawToken)
	if err != nil {
		return Claims{}, err
	}
	if c.Kind == KindRefresh {
		return Claims{}, ErrWrongTokenKind
	}
	if c.Kind == "" {
		if c.IssuedAt == nil || !c.IssuedAt.After(legacyKindFloor) {
			return Claims{}, ErrLegacyTokenExpired
		}
	}
	return c, nil
}

// RequireAuth validates the Authorization: Bearer <token> header and stores the
// claims in the request context.
//
// Every authenticated route runs through here, so this is the one place that
// decides a refresh token cannot be an API credential. A refresh token replayed
// as a Bearer would otherwise be a week of admin access that no logout and no
// session revocation touches, which is the whole reason kinds were added.
//
// The credential is accepted from the header only. An earlier version also read
// `?token=` from the query string, which put the JWT in the access log, the
// browser history, and the Referer header of any subresource the page loaded --
// every one of them a place a stolen token is read back out of. The query path
// is gone rather than deprecated: the report export that needed it now sends the
// header, and the two console builds that could still be holding a URL with a
// token in it get a 401 and a re-login rather than a credential that leaks for
// as long as anybody keeps the link.
//
// When a SessionStore is wired in (WithSessionStore), the token's `sid` must
// also name a live row. Without that check a signature is the whole
// authorisation: deactivating a user, demoting them, or revoking their session
// would leave the access token they already hold working until its own 15
// minutes ran out, and an access token a thief lifted from a browser is exactly
// the token that 15 minutes matters for.
func (s *JWTService) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawToken := ""
		h := r.Header.Get("Authorization")
		if h != "" {
			parts := strings.SplitN(h, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				rawToken = parts[1]
			}
		}
		if rawToken == "" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		claims, err := s.parseAccess(rawToken)
		if err != nil {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}
		// The session check is the difference between a token that is valid and
		// one that is still authorised. It runs on every authenticated request,
		// so it has to stay cheap: auth_sessions.jti is the primary key, and
		// this is one indexed point lookup.
		//
		// A token with no sid predates sessions and is trusted as-is; revoking
		// it would sign out every console that upgraded mid-session, and its
		// access-TTL is already short. An unset store means the same thing for
		// callers that never wired one in, and for the tests that construct a
		// JWTService directly.
		if s.sessions != nil && claims.SessionID != "" {
			ok, err := s.sessions.IsLive(r.Context(), claims.SessionID)
			if err != nil {
				// A broken read must not become an open door. Refusing is the
				// fail-closed choice; the health of the database is already
				// observable elsewhere, and a degraded but authenticated API is
				// worse than one that answers 503.
				log.Error().Err(err).
					Str("session_id", claims.SessionID).
					Msg("auth: read auth_sessions failed; refusing the request")
				writeErr(w, http.StatusServiceUnavailable, "session store unavailable")
				return
			}
			if !ok {
				http.Error(w, "invalid or expired token", http.StatusUnauthorized)
				return
			}
		}
		ctx := r.Context()
		ctx = context.WithValue(ctx, CtxUserID, claims.UserID)
		ctx = context.WithValue(ctx, CtxUsername, claims.Username)
		// The session id travels on the access token as `sid`, which is the only
		// place a request can learn which session it belongs to without asking
		// the database. Logout needs exactly that: the console that has lost its
		// refresh token still holds the access token, and that is the case with
		// no jti to revoke by.
		if claims.SessionID != "" {
			ctx = context.WithValue(ctx, CtxSessionID, claims.SessionID)
		}
		// rbac.RequireRole reads the role from its own key.
		ctx = rbac.WithRole(ctx, claims.Role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserIDFromContext returns the authenticated user's ID.
func UserIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(CtxUserID).(string); ok {
		return v
	}
	return ""
}

// SessionIDFromContext returns the auth session the request was authenticated
// with, or "" for a token minted before sessions existed. It is a session
// identifier, never an authorising credential: callers still have to scope any
// write to the user they got from UserIDFromContext.
func SessionIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(CtxSessionID).(string); ok {
		return v
	}
	return ""
}

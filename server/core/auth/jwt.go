package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenKind distinguishes the two credentials in a TokenPair.
type TokenKind string

const (
	// KindAccess is the short-lived credential sent as Authorization: Bearer on
	// every API call.
	KindAccess TokenKind = "access"
	// KindRefresh is the long-lived credential that only the refresh endpoint
	// accepts. It is never a valid Bearer token: without that separation, a
	// refresh token stolen from a browser's storage grants a week of API access
	// that no logout can withdraw.
	KindRefresh TokenKind = "refresh"
)

// Claims is the JWT payload for admin console users.
type Claims struct {
	UserID   string `json:"uid"`
	Username string `json:"usr"`
	Role     string `json:"rol"`
	// Kind is what makes the two tokens different credentials rather than the
	// same credential with two expiry times. Every verification checks it, so
	// the tokens cannot be substituted for one another.
	Kind TokenKind `json:"knd"`
	// SessionID is the auth_sessions row this token belongs to. It is empty on
	// an access token only when the token predates sessions; see ParseSession.
	SessionID string `json:"sid,omitempty"`
	jwt.RegisteredClaims
}

// JWTService issues and verifies access/refresh tokens.
type JWTService struct {
	secret          []byte
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
	// sessions is the server-side authority behind every access token. nil means
	// RequireAuth trusts the signature alone, which is the pre-session behaviour
	// and what the tests that build a JWTService directly still exercise.
	sessions *SessionStore
}

func NewJWTService(secret string, accessTTL, refreshTTL time.Duration) *JWTService {
	return &JWTService{secret: []byte(secret), accessTokenTTL: accessTTL, refreshTokenTTL: refreshTTL}
}

// WithSessionStore wires the session table into RequireAuth. Without it a token
// verifies on its signature for as long as it is unexpired, and nothing an
// administrator does -- deactivating the account, demoting the role, revoking
// the session -- reaches a console that is already holding one.
//
// This must be the same store login writes to. A second store against the same
// table is a second connection pool, but a second store against a different
// database is a second source of truth, and revocation would quietly stop
// applying to the tokens the middleware accepts.
func (s *JWTService) WithSessionStore(store *SessionStore) *JWTService {
	s.sessions = store
	return s
}

// TokenPair holds both issued tokens.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // unix seconds
}

// Issue creates a fresh token pair for a user. The refresh token's jti is
// returned as the session id so the caller can register the session.
//
// The access token is deliberately NOT given a jti: the jti is the handle a
// session is revoked by, and one that rotates on every refresh would name a row
// that no longer exists by the time it is used. The session id goes on the
// access token in its own `sid` field instead, where it names the row
// unambiguously for the whole life of the token. That field is what lets logout
// work for a console that still holds its access token and no longer has the
// refresh one -- the case where there is no jti anywhere to read.
//
// The `sid` claim carries no authority. RequireAuth does not read it, and every
// consumer of it in this package scopes its write to the user RequireAuth has
// already established, so presenting a token carrying someone else's sid
// revokes nothing.
func (s *JWTService) Issue(userID, username, role string) (TokenPair, error) {
	return s.IssueForSession(userID, username, role, "")
}

// IssueForSession issues a pair bound to sessionID. The caller mints sessionID,
// because the same value has to land on the auth_sessions row, in the refresh
// token's jti and in the access token's sid: three records that must name one
// session, so the caller has to be the one that creates it.
//
// An empty sessionID is tolerated for the callers that have no session to bind
// to (the route-contract suite). It produces a refresh token whose jti names no
// row, which is why the production paths in login.go always pass a value.
func (s *JWTService) IssueForSession(userID, username, role, sessionID string) (TokenPair, error) {
	now := time.Now()
	refreshJTI := sessionID
	if refreshJTI == "" {
		var err error
		refreshJTI, err = newJTI()
		if err != nil {
			return TokenPair{}, err
		}
	}

	access, err := s.sign(Claims{
		UserID:    userID,
		Username:  username,
		Role:      role,
		Kind:      KindAccess,
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   userID,
		},
	})
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign access token: %w", err)
	}

	refresh, err := s.sign(Claims{
		UserID:    userID,
		Username:  username,
		Role:      role,
		Kind:      KindRefresh,
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			// The jti IS the session id. Rotate writes the new row under the new
			// jti, and RevokeForUser matches on it, so a refresh token carrying
			// anything else names a row that does not exist.
			ID:        refreshJTI,
			ExpiresAt: jwt.NewNumericDate(now.Add(s.refreshTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   userID,
		},
	})
	if err != nil {
		return TokenPair{}, fmt.Errorf("sign refresh token: %w", err)
	}

	return TokenPair{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresAt:    now.Add(s.accessTokenTTL).Unix(),
	}, nil
}

func (s *JWTService) sign(c Claims) (string, error) {
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return t.SignedString(s.secret)
}

// Parse validates a token's signature, expiry and structure, and returns its
// claims. It does not check the token kind: a caller that cares -- every
// authentication path does -- must also check claims.Kind, which ParseKind does
// in one step so the check cannot be forgotten.
func (s *JWTService) Parse(tokenStr string) (Claims, error) {
	var c Claims
	t, err := jwt.ParseWithClaims(tokenStr, &c, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return s.secret, nil
	})
	if err != nil {
		return Claims{}, err
	}
	if !t.Valid {
		return Claims{}, errors.New("invalid token")
	}
	return c, nil
}

// ParseKind validates a token and requires it to be of the given kind. This is
// what stops a refresh token being presented as a Bearer credential, and an
// access token being redeemed for a new session.
func (s *JWTService) ParseKind(tokenStr string, want TokenKind) (Claims, error) {
	c, err := s.Parse(tokenStr)
	if err != nil {
		return Claims{}, err
	}
	if c.Kind != want {
		return Claims{}, ErrWrongTokenKind
	}
	return c, nil
}

// ErrWrongTokenKind is returned when a valid token is presented to an endpoint
// that does not accept its kind.
var ErrWrongTokenKind = errors.New("token is not of the required kind")

// ErrLegacyTokenExpired is returned for a token minted before kinds existed
// that is too old to be told apart from an access token. See parseAccess.
var ErrLegacyTokenExpired = errors.New("pre-kinds token is too old to authenticate")

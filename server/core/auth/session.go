package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
)

// Errors a caller may branch on. They are deliberately few: the session store
// exists to answer "may this refresh token still be used", and every refusal
// looks the same to the client on purpose -- a distinct error for "revoked"
// and one for "never existed" would tell someone probing for a stolen token
// exactly which answer they got.
var (
	// ErrSessionNotFound covers both "no such row" and "the row is not yours".
	// They are not separated because a caller that could tell them apart could
	// enumerate other users' session ids.
	ErrSessionNotFound = errors.New("session not found")
	// ErrSessionNotUsable means the row is revoked, expired, or has already
	// been rotated. Rotation is single-use by design, so a second attempt is a
	// replay, not a retry.
	ErrSessionNotUsable = errors.New("session is revoked, expired, or already rotated")
)

// Session is one row of auth_sessions. Revoked rows are kept, never deleted:
// deleting the row would make its token valid again, which is the exact inverse
// of what revoking it is for.
type Session struct {
	JTI        string         `db:"jti" json:"jti"`
	UserID     string         `db:"user_id" json:"-"`
	Username   string         `db:"username" json:"-"`
	Role       string         `db:"role" json:"-"`
	Revoked    bool           `db:"revoked" json:"-"`
	RevokedAt  *time.Time     `db:"revoked_at" json:"-"`
	RotatedTo  sql.NullString `db:"rotated_to" json:"-"`
	UserAgent  string         `db:"user_agent" json:"user_agent"`
	CreatedAt  time.Time      `db:"created_at" json:"created_at"`
	LastUsedAt time.Time      `db:"last_used_at" json:"last_used_at"`
	ExpiresAt  time.Time      `db:"expires_at" json:"expires_at"`
	CreatedIP  string         `db:"created_ip" json:"created_ip"`

	// Role is a snapshot taken at issue time and is intentionally not served to
	// the console. It is only ever compared against the live users row to detect
	// a promotion or demotion, and rendering it would show a role the user may
	// no longer hold.
}

// Live reports whether the row may still be exchanged for a new token. Both
// halves matter: a row can be revoked while its token is unexpired, and a row
// can outlive its token by however long a purge cycle is late.
func (s *Session) Live(now time.Time) bool {
	return !s.Revoked && s.ExpiresAt.After(now)
}

// SessionStore is the server-side authority for refresh tokens. The JWT says who
// a caller is; only this table says whether they still are allowed to renew.
type SessionStore struct {
	db *sqlx.DB
	// refreshTTL is stored rather than passed per call so that a session can
	// never outlive the token it backs, and so Rotate cannot be handed a caller
	// that made the two disagree.
	refreshTTL time.Duration
}

func NewSessionStore(db *sqlx.DB, refreshTTL time.Duration) *SessionStore {
	return &SessionStore{db: db, refreshTTL: refreshTTL}
}

const sessionColumns = `jti, user_id, username, role, revoked, revoked_at, rotated_to,
	user_agent, created_at, last_used_at, expires_at, created_ip`

// Create registers a new session for a freshly issued refresh token.
func (s *SessionStore) Create(ctx context.Context, userID, username, role, jti, userAgent, clientIP string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_sessions
			(jti, user_id, username, role, revoked, revoked_at, rotated_to,
			 user_agent, created_at, last_used_at, expires_at, created_ip)
		VALUES (?, ?, ?, ?, 0, NULL, NULL, ?, ?, ?, ?, ?)`,
		jti, userID, username, role, userAgent, now, now, now.Add(s.refreshTTL), clientIP)
	return err
}

// Get returns the session row for a jti, revoked or expired ones included: the
// refresh path has to be able to tell "this token was used" from "this token
// was never issued", and that distinction is not visible without the row.
func (s *SessionStore) Get(ctx context.Context, jti string) (*Session, error) {
	var sess Session
	err := s.db.GetContext(ctx, &sess, `SELECT `+sessionColumns+` FROM auth_sessions WHERE jti = ?`, jti)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// IsLive answers the question every authenticated request asks: does this
// session still authorise anything? It is the read RequireAuth makes, so it is
// a single indexed lookup on the primary key rather than a full row decode --
// it returns a bool and reads two columns, because it runs on every API call
// and nothing else about the row is needed there.
//
// A missing row is false, not an error. A session row is deleted only by
// PurgeExpired after expiry, and an access token whose session is gone is a
// token that has outlived its session, which means refusing it rather than
// treating it as legacy. The distinction from a real read failure matters:
// errors fail closed at the caller, but they are also reported, so an outage is
// visible instead of looking like a fleet-wide logout.
func (s *SessionStore) IsLive(ctx context.Context, jti string) (bool, error) {
	var ok int
	err := s.db.GetContext(ctx, &ok, `
		SELECT 1 FROM auth_sessions
		WHERE jti = ? AND revoked = 0 AND expires_at > ?`,
		jti, time.Now().UTC())
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Rotate consumes oldJTI and creates its replacement in a single transaction.
//
// The two halves are one unit on purpose. Marking the old row revoked without
// creating the new one strands the user with no working credential; creating the
// new row first would briefly mint a second live session for the same browser,
// which is the window a replay attacker needs. So the successor is only ever
// born in the same commit that kills its predecessor, and if the update matches
// no live row the whole transaction is discarded.
//
// The replacement inherits identity (user, username, role, agent, address) from
// the row it replaces, so rotation cannot be used to change any of it. Callers
// must have checked the role against the live users row first: the copied value
// is the one that was verified, not a fresh lookup.
//
// One rotation wins. A concurrent second rotation of the same jti finds no live
// row, rolls back, and returns ErrSessionNotUsable.
func (s *SessionStore) Rotate(ctx context.Context, oldJTI, newJTI string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var prev Session
	if err := tx.GetContext(ctx, &prev, `SELECT `+sessionColumns+` FROM auth_sessions WHERE jti = ?`, oldJTI); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSessionNotUsable
		}
		return err
	}

	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
		UPDATE auth_sessions SET revoked = 1, revoked_at = ?, rotated_to = ?, last_used_at = ?
		WHERE jti = ? AND revoked = 0 AND expires_at > ?`, now, newJTI, now, oldJTI, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrSessionNotUsable
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auth_sessions
			(jti, user_id, username, role, revoked, revoked_at, rotated_to,
			 user_agent, created_at, last_used_at, expires_at, created_ip)
		VALUES (?, ?, ?, ?, 0, NULL, NULL, ?, ?, ?, ?, ?)`,
		newJTI, prev.UserID, prev.Username, prev.Role, prev.UserAgent,
		now, now, now.Add(s.refreshTTL), prev.CreatedIP); err != nil {
		return err
	}
	return tx.Commit()
}

// Revoke withdraws a single session. Revoking an already-revoked or unknown jti
// is not an error: the caller's intent -- this token must not work -- already
// holds, and a logout that failed because it was already done would be noise.
func (s *SessionStore) Revoke(ctx context.Context, jti string) error {
	return s.revoke(ctx, jti, "", false)
}

// RevokeForUser is Revoke with the ownership check folded into the same UPDATE.
// Doing it as a separate SELECT-then-UPDATE would leave a window in which a
// session row could change owner between the check and the write, and the answer
// must be one statement to be worth anything.
func (s *SessionStore) RevokeForUser(ctx context.Context, jti, userID string) error {
	return s.revoke(ctx, jti, userID, true)
}

func (s *SessionStore) revoke(ctx context.Context, jti, userID string, scoped bool) error {
	query := `UPDATE auth_sessions SET revoked = 1, revoked_at = ? WHERE jti = ? AND revoked = 0`
	args := []any{time.Now().UTC(), jti}
	if scoped {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	// A scoped write that matched nothing means the row is not the caller's --
	// or is gone, or already revoked. It is reported the same way in all three
	// cases, so the answer never becomes an existence oracle. It is also NOT
	// reported as success: that would tell the caller their request landed when
	// nothing happened.
	if scoped {
		return ErrSessionNotFound
	}
	// Unscoped, a miss can only be "already revoked" or "no such row", and the
	// caller only needs to know that the token will not work either way.
	var n int
	if err := s.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM auth_sessions WHERE jti = ?`, jti); err != nil {
		return err
	}
	if n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// RevokeAllForUser withdraws every live session a user holds.
//
// This is the response to a refresh token that is replayed, forged, or belongs
// to an account that was disabled or had its role changed. In each of those
// cases the honest answer to "who else is holding this user's tokens" is "assume
// all of them", because the server cannot tell the legitimate browser from the
// thief that took the token.
func (s *SessionStore) RevokeAllForUser(ctx context.Context, userID string) error {
	if userID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE auth_sessions SET revoked = 1, revoked_at = ? WHERE user_id = ? AND revoked = 0`,
		time.Now().UTC(), userID)
	return err
}

// ListActive returns the caller's live sessions, newest use first. The result is
// never nil: the console renders it directly and a null array is a blank page
// with no error anywhere.
func (s *SessionStore) ListActive(ctx context.Context, userID string) ([]Session, error) {
	rows := []Session{}
	err := s.db.SelectContext(ctx, &rows, `SELECT `+sessionColumns+`
		FROM auth_sessions
		WHERE user_id = ? AND revoked = 0 AND expires_at > ?
		ORDER BY last_used_at DESC`, userID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// PurgeExpired deletes rows whose TTL has passed.
//
// Only expired rows go. A revoked row is the record that a used or stolen token
// must keep being refused, and dropping it early would make that token look
// unissued again -- the one case where "clean up the table" and "stay secure"
// point in opposite directions. Expiry is also the safe moment to delete: a
// row's expires_at is written at or after its token's exp, so nothing still
// verifiable is ever removed.
func (s *SessionStore) PurgeExpired(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at <= ?`, time.Now().UTC())
	return err
}

// RoleFor implements RoleReader against the live users row.
//
// An account that has been deactivated or stripped of its credential is an
// error rather than a role: the caller wants to know whether a session may be
// continued, and "yes, as a viewer" is the wrong answer for a user who has no
// password left. The password column is read for exactly that reason -- the row
// existing is not the same as the account being usable.
func (s *SessionStore) RoleFor(ctx context.Context, userID string) (string, string, error) {
	var u struct {
		Username    string `db:"username"`
		Role        string `db:"role"`
		HasPassword bool   `db:"has_password"`
		IsActive    int    `db:"is_active"`
	}
	err := s.db.GetContext(ctx, &u, `
		SELECT username, role, password_hash <> '' AS has_password,
		       COALESCE(is_active, 1) AS is_active
		FROM users WHERE id = ?`, userID)
	if err != nil {
		return "", "", err
	}
	if u.IsActive != 1 || !u.HasPassword {
		return "", "", ErrUserNotActive
	}
	return u.Username, u.Role, nil
}

// ErrUserNotActive is returned by RoleFor for an account that exists but may no
// longer hold a session. Distinct from sql.ErrNoRows so a caller can tell a
// deleted user from a disabled one, neither of which is an error it can act on.
var ErrUserNotActive = errors.New("user is not active")

// pathJTI reads the {jti} path segment from either router. ServeMux publishes
// matched wildcards through the stdlib PathValue; chi keeps its own params and
// populates neither, so a single lookup cannot serve both and the second one is
// the only chi reference in this package -- a dep the chiRouter interface above
// exists to keep optional.
func pathJTI(r *http.Request) string {
	if v := r.PathValue("jti"); v != "" {
		return v
	}
	return chi.URLParam(r, "jti")
}

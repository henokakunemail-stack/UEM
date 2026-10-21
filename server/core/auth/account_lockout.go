package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"
)

// Per-account lockout, kept in the database so it holds across server nodes.
//
// The per-IP limiter lives in each node's memory: with two nodes an attacker
// gets twice the guesses, and spreading guesses across many addresses defeats
// it outright. This counts failures per username instead, wherever they arrive.
//
// The trade-off is deliberate and bounded: anyone can lock an account by
// guessing wrong on purpose, so the lock is short and expires on its own. An
// operator locked out by an attack waits AccountLockout, which is far better
// than an attacker who never has to wait at all.
const (
	AccountMaxFailures = 10
	AccountWindow      = 15 * time.Minute
	AccountLockout     = 15 * time.Minute
)

type accountLockout struct {
	db *sqlx.DB
}

func lockoutKey(username string) string { return strings.ToLower(strings.TrimSpace(username)) }

// lockedFor reports how long the account stays locked, or 0.
func (a accountLockout) lockedFor(ctx context.Context, username string) time.Duration {
	if a.db == nil {
		return 0
	}
	var until sql.NullTime
	err := a.db.GetContext(ctx, &until, `SELECT locked_until FROM login_failures WHERE username = ?`, lockoutKey(username))
	if err != nil || !until.Valid {
		return 0
	}
	if d := time.Until(until.Time); d > 0 {
		return d
	}
	return 0
}

// recordFailure counts one failed sign-in and locks the account when the count
// within the window reaches the limit. Errors are logged, never returned: a
// lockout table that cannot be written must not turn a wrong password into a
// server error, and the per-IP limiter still applies.
// recordFailure counts one failed attempt and reports whether this attempt is
// the one that locked the account, so the lock can be audited exactly once.
func (a accountLockout) recordFailure(ctx context.Context, username string) (lockedNow bool) {
	if a.db == nil {
		return false
	}
	key := lockoutKey(username)
	now := time.Now().UTC()

	// Increment inside the current window. Atomic on its own, so two nodes
	// counting the same account at once both count.
	res, err := a.db.ExecContext(ctx, `
		UPDATE login_failures SET failures = failures + 1
		 WHERE username = ? AND window_started_at > ?`, key, now.Add(-AccountWindow))
	if err != nil {
		log.Warn().Err(err).Msg("record login failure")
		return false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// No row, or one from an expired window: start a fresh window. A
		// concurrent insert from another node makes this fail on the primary
		// key, and then the increment above is retried once.
		if _, err := a.db.ExecContext(ctx, `DELETE FROM login_failures WHERE username = ? AND window_started_at <= ?`,
			key, now.Add(-AccountWindow)); err != nil {
			log.Warn().Err(err).Msg("reset login failure window")
		}
		if _, err := a.db.ExecContext(ctx, `
			INSERT INTO login_failures (username, failures, window_started_at) VALUES (?, 1, ?)`, key, now); err != nil {
			if _, err := a.db.ExecContext(ctx, `UPDATE login_failures SET failures = failures + 1 WHERE username = ?`, key); err != nil {
				log.Warn().Err(err).Msg("record login failure")
				return false
			}
		}
	}

	// Matches only while the account is not already locked, so exactly one
	// failure -- the one that crosses the limit -- sees a row change.
	lock, err := a.db.ExecContext(ctx, `
		UPDATE login_failures SET locked_until = ?
		 WHERE username = ? AND failures >= ? AND (locked_until IS NULL OR locked_until < ?)`,
		now.Add(AccountLockout), key, AccountMaxFailures, now)
	if err != nil {
		log.Warn().Err(err).Msg("lock account after failed logins")
		return false
	}
	n, _ := lock.RowsAffected()
	return n == 1
}

// reset clears the count after a successful sign-in.
func (a accountLockout) reset(ctx context.Context, username string) {
	if a.db == nil {
		return
	}
	if _, err := a.db.ExecContext(ctx, `DELETE FROM login_failures WHERE username = ?`, lockoutKey(username)); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		log.Warn().Err(err).Msg("reset login failures")
	}
}

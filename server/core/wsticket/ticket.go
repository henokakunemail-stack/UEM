// Package wsticket issues one-time tickets that a WebSocket handshake can
// exchange for a real credential.
//
// The credential it replaces is a JWT passed as ?token= on the socket URL,
// which is the only credential a browser can attach to a WebSocket handshake.
// A query string is the worst place to keep a credential: it is written to the
// web server's access log, kept in browser history, forwarded in Referer, and
// copied into every proxy log between here and the client. A JWT captured that
// way is a valid credential for its whole TTL and there is no row to revoke.
//
// A ticket closes that gap without needing a header. The client fetches one over
// an authenticated request, puts it in the URL, and the server destroys it at
// the moment of the handshake. What lands in the access log is a value that is
// already spent, and what the log reader would need to escalate -- the JWT --
// never left the TLS request body.
package wsticket

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
)

// Purposes. These are opaque to the store, which only guarantees that a ticket
// minted for one cannot be redeemed for another. They are constants because the
// guarantee is worthless if the minting side and the redeeming side disagree on
// the spelling: a mismatch fails closed and looks like a lost login.
const (
	PurposeRemoteExec    = "remote-exec"
	PurposeRemoteDesktop = "remote-desktop"
	PurposeAgentEnroll   = "agent-enroll"
)

// Defaults for the constructor and for Issue.
const (
	// DefaultTTL is the window between fetching a ticket and the handshake that
	// spends it. It is short on purpose: the ticket is not a session, it only
	// has to survive the round trip that started by handing it out. Every
	// additional second is a second during which a copy of it from a log line
	// is still redeemable.
	DefaultTTL = 60 * time.Second

	// DefaultMaxLive bounds the table. Every ticket is a row and every row
	// costs a write on a console page load, so an authenticated user who can
	// mint without limit has a way to turn "open a terminal" into "fill the
	// disk". The number is far above what any real fleet needs in one TTL
	// window and exists to stop the unbounded case, not to be tuned.
	DefaultMaxLive = 10000
)

var (
	// ErrUnknownTicket covers every way a redeem can fail: no such ticket,
	// already spent, expired, or minted for a different purpose. They are one
	// error because a caller that could tell them apart could use the endpoint
	// to test whether a ticket it holds is live, and the ticket is the only
	// secret in the exchange.
	ErrUnknownTicket = errors.New("ticket is unknown, expired, or already used")

	// ErrStoreFull is returned instead of a ticket once the live row count
	// reaches the configured cap. It is a distinct error because the cause is
	// server state, not a bad ticket, and the caller can usefully retry later.
	ErrStoreFull = errors.New("ticket store is at capacity")
)

// Store is the server-side authority on outstanding WebSocket tickets.
type Store struct {
	db      *sqlx.DB
	maxLive int
}

// NewStore returns a store holding at most maxLive outstanding tickets. A
// non-positive maxLive takes DefaultMaxLive rather than meaning "unlimited":
// the cap is the only thing standing between an authenticated user and a table
// that only grows, so there is no value of this argument that should disable it.
func NewStore(db *sqlx.DB, maxLive int) *Store {
	if maxLive <= 0 {
		maxLive = DefaultMaxLive
	}
	return &Store{db: db, maxLive: maxLive}
}

// newTicket returns 256 bits of randomness in the URL-safe alphabet. The size
// is not tuned: a ticket is redeemed once and lives 60 seconds, so it needs to
// be unguessable rather than short, and 128 bits would already be enough --
// 32 bytes leaves no room for a future mistake to weaken it. Raw URL encoding
// because the value is destined for a query string, where '+' and '/' would
// need escaping and '=' padding would be noise.
func newTicket() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashTicket maps a ticket to its stored primary key.
//
// The table is what a backup, a replica, or a support bundle carries, and those
// get copied around far more freely than the log files this package exists to
// keep credentials out of. Storing the digest rather than the ticket means such
// a copy is a list of already-spent values: whoever has it still has to guess a
// 256-bit secret. Storing the ticket would hand them every outstanding
// handshake authentication for the TTL.
//
// SHA-256 rather than a slow KDF: the input is 256 bits of uniform randomness,
// so there is no dictionary to grind, and Consume is on the handshake path
// where a deliberate key-stretch would be paid by every connection.
func hashTicket(ticket string) string {
	sum := sha256.Sum256([]byte(ticket))
	return hex.EncodeToString(sum[:])
}

// Issue mints a ticket redeemable once, for purpose, as subject. It returns the
// plaintext; the store keeps only its hash, so a lost ticket cannot be recovered
// and only the caller holding this value can spend it.
//
// A non-positive ttl is taken as DefaultTTL rather than rejected. A zero
// lifetime from a mis-wired constant would otherwise mint a ticket that is dead
// on arrival, which reaches the user as a socket that closes during the
// handshake with nothing in the logs to explain it.
func (s *Store) Issue(ctx context.Context, subject, purpose string, ttl time.Duration) (string, error) {
	if subject == "" || purpose == "" {
		return "", errors.New("wsticket: subject and purpose are required")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	ticket, err := newTicket()
	if err != nil {
		return "", err
	}

	// Purging, counting and inserting share one transaction so the cap counts
	// something. Read-then-write would let N concurrent callers all observe a
	// count under the cap and all insert, so the bound would be exceeded by
	// however many requests the caller could fan out -- which is the whole
	// attack the cap exists for. The transaction is on one pooled connection,
	// and db.Open pins MaxOpenConns(1), so in-process issuers serialise here.
	// Across processes SQLite still serialises the writes; two servers can
	// overshoot the cap by the number racing in that instant, which is a
	// rounding error on a bound meant to stop unbounded growth.
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `DELETE FROM ws_tickets WHERE expires_at <= ?`, now); err != nil {
		return "", err
	}
	var live int
	if err := tx.GetContext(ctx, &live, `SELECT COUNT(*) FROM ws_tickets`); err != nil {
		return "", err
	}
	if live >= s.maxLive {
		// The purge is committed even though the issuance is refused. Rolling
		// back would discard the cleanup that was about to make room, so a store
		// that hit the cap under load would stay full for as long as the load
		// lasted -- a self-inflicted denial of service on the way to preventing
		// one.
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return "", ErrStoreFull
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ws_tickets (ticket_hash, subject, purpose, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`,
		hashTicket(ticket), subject, purpose, now, now.Add(ttl)); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return ticket, nil
}

// Consume redeems a ticket for whatever purpose it was minted for. Callers that
// know which socket they are opening should use ConsumeFor, which will not let
// a ticket minted for a different purpose through.
func (s *Store) Consume(ctx context.Context, ticket string) (subject, purpose string, err error) {
	return s.consume(ctx, ticket, "")
}

// ConsumeFor redeems a ticket and refuses one minted for another purpose.
//
// The purpose test is in the DELETE predicate rather than in a check after the
// row is read, so a wrong-purpose attempt neither consumes the ticket nor
// leaves a window in which the row could be re-read. Not consuming it is
// deliberate: the holder of an unguessable ticket who presents it for the wrong
// purpose has almost certainly found it in a log, and burning a legitimate
// client's only way to connect would turn one leak into an outage.
func (s *Store) ConsumeFor(ctx context.Context, ticket, purpose string) (subject string, err error) {
	if purpose == "" {
		// Falling through to the unscoped path here would silently disable the
		// check this method exists to make, and a ticket minted for agent
		// enrolment would open a remote desktop.
		return "", errors.New("wsticket: purpose is required")
	}
	sub, _, err := s.consume(ctx, ticket, purpose)
	return sub, err
}

// consume deletes the row and returns what it held, in one statement.
//
// The delete is the redemption: SQLite runs DELETE ... RETURNING as a single
// write, so of two callers racing on one ticket the first to commit takes the
// row and the second's predicate matches nothing. Reading the row first and
// deleting it afterwards would be a different shape -- both callers could see
// a live row and both proceed, and the second delete would be reported as
// success against a table that no longer had anything to spend.
func (s *Store) consume(ctx context.Context, ticket, purpose string) (subject, used string, err error) {
	if ticket == "" {
		return "", "", ErrUnknownTicket
	}
	// The expiry test is in the predicate for the same reason the delete is:
	// an expired row is removed by the attempt, so it cannot be probed, counted
	// as live by a cap check, or read by a caller holding it open.
	query := `DELETE FROM ws_tickets WHERE ticket_hash = ? AND expires_at > ?`
	args := []any{hashTicket(ticket), time.Now().UTC()}
	if purpose != "" {
		query += ` AND purpose = ?`
		args = append(args, purpose)
	}
	query += ` RETURNING subject, purpose`

	if err := s.db.QueryRowxContext(ctx, query, args...).Scan(&subject, &used); err != nil {
		// sql.ErrNoRows is the successful shape of every failure here.
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrUnknownTicket
		}
		return "", "", err
	}
	return subject, used, nil
}

// PurgeExpired drops rows past their TTL. Issue does this on its own path; this
// exists so an operator's existing janitor loop can call it and so a store that
// went quiet while full drains without waiting for the next issuance.
func (s *Store) PurgeExpired(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM ws_tickets WHERE expires_at <= ?`, time.Now().UTC())
	return err
}

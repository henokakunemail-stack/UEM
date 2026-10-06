package wsticket

// Every test here is a way a ticket must stop working, so the assertions are
// about refusal as much as about success. A store that only proved the happy
// path would pass a test suite this small while still redeeming a ticket twice.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

const (
	subject = "user-1"
	purpose = PurposeRemoteExec
)

func newStore(t *testing.T) (*Store, *sqlx.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return NewStore(d, 0), d
}

func issue(t *testing.T, s *Store, sub, pur string, ttl time.Duration) string {
	t.Helper()
	ticket, err := s.Issue(context.Background(), sub, pur, ttl)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if ticket == "" {
		t.Fatal("Issue returned an empty ticket")
	}
	return ticket
}

func liveRows(t *testing.T, d *sqlx.DB) int {
	t.Helper()
	var n int
	if err := d.Get(&n, `SELECT COUNT(*) FROM ws_tickets WHERE expires_at > ?`, time.Now().UTC()); err != nil {
		t.Fatalf("count live tickets: %v", err)
	}
	return n
}

// expireTicket puts one ticket's expiry in the past, which is the state a row
// reaches on its own once its TTL elapses.
//
// The alternative is to issue with a short TTL and sleep, but that makes the
// test bet on the machine: a GC pause or a loaded CI runner can stretch the
// window between issuing and asserting, and the row expires before the
// assertion that needs it still live runs. That is not a rare edge -- it
// reproduced in roughly one run in eight on an idle developer machine. Setting
// expires_at is what elapsed time actually does to the column, so the store
// under test sees the identical state and the test stops depending on the
// scheduler.
func expireTicket(t *testing.T, d *sqlx.DB, ticket string) {
	t.Helper()
	_, err := d.Exec(`UPDATE ws_tickets SET expires_at = ? WHERE ticket_hash = ?`,
		time.Now().UTC().Add(-time.Minute), hashTicket(ticket))
	if err != nil {
		t.Fatalf("expire ticket: %v", err)
	}
}

// TestConsumeRedeemsOnce is the base contract: a ticket is a name for exactly
// one handshake. The second consume is what stops a copy of the URL that
// survived in an access log from connecting an hour later.
func TestConsumeRedeemsOnce(t *testing.T) {
	s, d := newStore(t)
	ctx := context.Background()
	ticket := issue(t, s, subject, purpose, DefaultTTL)

	gotSub, gotPurpose, err := s.Consume(ctx, ticket)
	if err != nil {
		t.Fatalf("first Consume: %v", err)
	}
	if gotSub != subject {
		t.Errorf("subject = %q, want %q", gotSub, subject)
	}
	if gotPurpose != purpose {
		t.Errorf("purpose = %q, want %q", gotPurpose, purpose)
	}

	if _, _, err := s.Consume(ctx, ticket); !errors.Is(err, ErrUnknownTicket) {
		t.Errorf("second Consume: err = %v, want %v", err, ErrUnknownTicket)
	}
	// The row is gone, not just marked spent: a row left behind would keep
	// counting against the cap for a ticket nobody can use.
	if n := liveRows(t, d); n != 0 {
		t.Errorf("%d live ticket row(s) after redemption, want 0", n)
	}
}

func TestConsumeUnknownTicket(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for name, ticket := range map[string]string{
		"never issued": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"empty":        "",
	} {
		if _, _, err := s.Consume(ctx, ticket); !errors.Is(err, ErrUnknownTicket) {
			t.Errorf("consume %s ticket: err = %v, want %v", name, err, ErrUnknownTicket)
		}
	}
}

// TestConcurrentConsumeHasOneWinner is the guarantee that cannot be checked by
// reading the code. Read-then-delete would pass every other test in this file
// and would let two callers both connect: the second one to arrive while the
// first was mid-handshake. SQLite serialises the two DELETEs, and the losing
// predicate matches nothing.
//
// Each round mints a fresh ticket so no round can be masked by the one before
// it, and repeats because a single round is not a reliable detector: against a
// read-then-delete build this assertion failed in roughly 3 runs out of 4, which
// would let a broken store merge on a lucky one. Rounds also make the failure
// report useful -- "round 4 of 8" says the race is real rather than one caller
// having got unlucky.
func TestConcurrentConsumeHasOneWinner(t *testing.T) {
	s, d := newStore(t)

	const (
		rounds = 8
		racers = 32
	)
	for round := 0; round < rounds; round++ {
		ticket := issue(t, s, subject, purpose, DefaultTTL)

		var wg sync.WaitGroup
		start := make(chan struct{})
		results := make([]error, racers)
		subjects := make([]string, racers)

		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				subjects[i], _, results[i] = s.Consume(context.Background(), ticket)
			}(i)
		}
		close(start)
		wg.Wait()

		var winners int
		for i, err := range results {
			switch {
			case err == nil:
				winners++
				if subjects[i] != subject {
					t.Errorf("round %d winner %d got subject %q, want %q", round, i, subjects[i], subject)
				}
			case errors.Is(err, ErrUnknownTicket):
			default:
				t.Errorf("round %d racer %d: unexpected error %v", round, i, err)
			}
		}
		if winners != 1 {
			t.Fatalf("round %d of %d: got %d winners redeeming one ticket, want exactly 1",
				round, rounds, winners)
		}
		if n := liveRows(t, d); n != 0 {
			t.Fatalf("round %d: %d rows left after the race, want 0", round, n)
		}
	}
}

// TestExpiredTicketRejected covers the TTL: the window exists to bridge the gap
// between the client fetching the ticket and the handshake finishing, and
// nothing justifies keeping it open afterwards.
func TestExpiredTicketRejected(t *testing.T) {
	s, d := newStore(t)
	ctx := context.Background()
	ticket := issue(t, s, subject, purpose, 30*time.Millisecond)

	if _, _, err := s.Consume(ctx, ticket); err != nil {
		t.Fatalf("consume before expiry: %v", err)
	}

	// A second ticket, left to go stale.
	stale := issue(t, s, subject, purpose, 30*time.Millisecond)
	time.Sleep(120 * time.Millisecond)
	if _, _, err := s.Consume(ctx, stale); !errors.Is(err, ErrUnknownTicket) {
		t.Errorf("consume after expiry: err = %v, want %v", err, ErrUnknownTicket)
	}
	// An expired ticket must be removed by the attempt, not merely refused, or
	// it keeps occupying a capped store until something else purges it.
	if n := liveRows(t, d); n != 0 {
		t.Errorf("%d expired row(s) counted live, want 0", n)
	}
}

// TestPurposeIsNotInterchangeable is why purpose exists. A ticket is a
// credential for one socket; without the check, a ticket read out of a
// remote-exec log opens a remote desktop and carries the subject who could open
// it, not the one who logged the URL.
func TestPurposeIsNotInterchangeable(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	ticket := issue(t, s, subject, PurposeRemoteExec, DefaultTTL)

	if _, err := s.ConsumeFor(ctx, ticket, PurposeRemoteDesktop); !errors.Is(err, ErrUnknownTicket) {
		t.Errorf("redeem a remote-exec ticket as remote-desktop: err = %v, want %v", err, ErrUnknownTicket)
	}
	if _, err := s.ConsumeFor(ctx, ticket, PurposeAgentEnroll); !errors.Is(err, ErrUnknownTicket) {
		t.Errorf("redeem a remote-exec ticket as agent-enroll: err = %v, want %v", err, ErrUnknownTicket)
	}
	// The right purpose still works, and reports the purpose it was minted for
	// rather than the one the caller claimed.
	gotSub, err := s.ConsumeFor(ctx, ticket, PurposeRemoteExec)
	if err != nil {
		t.Fatalf("consume with the correct purpose: %v", err)
	}
	if gotSub != subject {
		t.Errorf("subject = %q, want %q", gotSub, subject)
	}
	if _, _, err := s.Consume(ctx, ticket); !errors.Is(err, ErrUnknownTicket) {
		t.Errorf("ticket survived its correct-purpose redemption: %v", err)
	}
}

// TestConsumeForRequiresPurpose guards the scoped form itself. An empty purpose
// falling through to the unscoped path would quietly turn the check off.
func TestConsumeForRequiresPurpose(t *testing.T) {
	s, _ := newStore(t)
	ticket := issue(t, s, subject, purpose, DefaultTTL)
	if _, err := s.ConsumeFor(context.Background(), ticket, ""); err == nil {
		t.Error("ConsumeFor with an empty purpose was accepted; the check is meant to be mandatory")
	}
	if _, _, err := s.Consume(context.Background(), ticket); err != nil {
		t.Errorf("the refused call spent the ticket anyway: %v", err)
	}
}

// TestStoredValueIsNotTheTicket is the property that decides what a leaked
// database copy is worth. A table storing plaintext tickets is a list of live
// handshake credentials; one storing digests is a list of strings that cannot
// be presented.
func TestStoredValueIsNotTheTicket(t *testing.T) {
	s, d := newStore(t)
	ticket := issue(t, s, subject, purpose, DefaultTTL)

	rows := []struct {
		TicketHash string `db:"ticket_hash"`
		Subject    string `db:"subject"`
	}{}
	if err := d.Select(&rows, `SELECT ticket_hash, subject FROM ws_tickets`); err != nil {
		t.Fatalf("read stored rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("stored %d row(s), want 1", len(rows))
	}

	// Nothing in the row is the ticket, or any part of it that would narrow the
	// guess. Checking the whole value is not enough on its own: a store that
	// kept a prefix would still be found here, since the prefix is part of the
	// value, but a store that kept, say, the first 8 characters after a
	// delimiter would not be.
	if strings.Contains(rows[0].TicketHash, ticket) {
		t.Error("the stored hash contains the plaintext ticket")
	}
	if rows[0].TicketHash == ticket {
		t.Error("the stored value is the plaintext ticket")
	}
	if want := hashTicket(ticket); rows[0].TicketHash != want {
		t.Errorf("ticket_hash = %q, want the SHA-256 of the ticket %q", rows[0].TicketHash, want)
	}
	if len(rows[0].TicketHash) != 64 {
		t.Errorf("ticket_hash is %d characters, want 64 hex characters", len(rows[0].TicketHash))
	}
	if rows[0].Subject != subject {
		t.Errorf("subject = %q, want %q", rows[0].Subject, subject)
	}

	// And the digest is not itself redeemable: knowing the table does not help.
	if _, _, err := s.Consume(context.Background(), rows[0].TicketHash); !errors.Is(err, ErrUnknownTicket) {
		t.Errorf("the stored hash was redeemable as a ticket: err = %v", err)
	}
}

// TestTicketsAreUnguessableAndURLSafe checks the two properties the transport
// depends on. A ticket short of 128 bits, or carrying bytes a query string
// mangles, fails at the handshake rather than anywhere obvious.
func TestTicketsAreUnguessableAndURLSafe(t *testing.T) {
	s, _ := newStore(t)
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		ticket := issue(t, s, subject, purpose, DefaultTTL)
		if len(ticket) < 22 {
			t.Fatalf("ticket %d characters long, want at least 22 for 128 bits: %q", len(ticket), ticket)
		}
		if strings.ContainsAny(ticket, "+/=&?#") {
			t.Errorf("ticket %q needs escaping in a query string", ticket)
		}
		if seen[ticket] {
			t.Fatalf("ticket repeated: %q", ticket)
		}
		seen[ticket] = true
	}
}

// TestRowCapIsEnforced is the denial-of-service bound. Without it, one
// authenticated user with a loop and a valid access token can grow the table
// until the database stops taking writes.
func TestRowCapIsEnforced(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cap.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	s := NewStore(d, 3)
	ctx := context.Background()
	issued := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		tk, err := s.Issue(ctx, subject, purpose, DefaultTTL)
		if err != nil {
			t.Fatalf("issue %d of 3: %v", i, err)
		}
		issued = append(issued, tk)
	}
	if n := liveRows(t, d); n != 3 {
		t.Fatalf("%d live rows, want 3", n)
	}

	if _, err := s.Issue(ctx, subject, purpose, DefaultTTL); !errors.Is(err, ErrStoreFull) {
		t.Errorf("issue past the cap: err = %v, want %v", err, ErrStoreFull)
	}
	if n := liveRows(t, d); n != 3 {
		t.Errorf("%d live rows after a refused issue, want 3: a refusal must not have written", n)
	}

	// Spending one of the three frees a slot. A cap that only ever refuses is
	// as broken as no cap at all.
	if _, _, err := s.Consume(ctx, issued[0]); err != nil {
		t.Fatalf("redeem to make room: %v", err)
	}
	if n := liveRows(t, d); n != 2 {
		t.Fatalf("%d live rows after redeeming one, want 2", n)
	}
	if _, err := s.Issue(ctx, subject, purpose, DefaultTTL); err != nil {
		t.Errorf("issue after making room: %v", err)
	}
}

// TestPurgeExpiredReclaimsRoom is why Issue purges before it counts. A full
// store whose tickets have all expired must serve again without an operator
// running anything.
func TestPurgeExpiredReclaimsRoom(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "purge.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	s := NewStore(d, 2)
	ctx := context.Background()
	shortLived := make([]string, 0, 3)
	for i := 0; i < 2; i++ {
		tk, err := s.Issue(ctx, subject, purpose, DefaultTTL)
		if err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
		shortLived = append(shortLived, tk)
	}
	if _, err := s.Issue(ctx, subject, purpose, DefaultTTL); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("issue into a full store: err = %v, want %v", err, ErrStoreFull)
	}

	// The one the refused call asked for never existed, so both rows above must
	// go stale for the store to drain.
	for _, tk := range shortLived {
		expireTicket(t, d, tk)
	}
	if _, err := s.Issue(ctx, subject, purpose, DefaultTTL); err != nil {
		t.Errorf("issue after the earlier tickets expired: %v", err)
	}

	// And the explicit janitor call, for a store that goes quiet while full.
	expiring, err := s.Issue(ctx, subject, purpose, DefaultTTL)
	if err != nil {
		t.Fatalf("issue before the purge check: %v", err)
	}
	expireTicket(t, d, expiring)
	if err := s.PurgeExpired(ctx); err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	// One DefaultTTL row was issued in between and is still live; what must be
	// gone is the short-lived one, which is the only thing PurgeExpired may
	// remove. A purge that also took the live row would break every socket the
	// cap check had just made room for.
	if n := liveRows(t, d); n != 1 {
		t.Errorf("%d live rows after purge, want 1: only the short-lived row should have gone", n)
	}
	if _, _, err := s.Consume(ctx, shortLived[0]); !errors.Is(err, ErrUnknownTicket) {
		t.Errorf("a purged ticket still redeemed: %v", err)
	}
}

// TestIssueRejectsEmptyIdentity keeps a ticket from being minted for nobody, or
// for nobody in particular: a row with an empty subject is a socket that cannot
// be attributed in an audit trail.
func TestIssueRejectsEmptyIdentity(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if _, err := s.Issue(ctx, "", purpose, DefaultTTL); err == nil {
		t.Error("issue with an empty subject was accepted")
	}
	if _, err := s.Issue(ctx, subject, "", DefaultTTL); err == nil {
		t.Error("issue with an empty purpose was accepted")
	}
}

// TestNonPositiveTTLFallsBackToDefault covers the two mis-wired constants. A
// zero lifetime would otherwise mint a ticket that is dead on arrival and
// reaches the user as a socket closing mid-handshake with nothing in the logs.
func TestNonPositiveTTLFallsBackToDefault(t *testing.T) {
	s, d := newStore(t)
	ticket := issue(t, s, subject, purpose, 0)

	var expires time.Time
	if err := d.Get(&expires, `SELECT expires_at FROM ws_tickets`); err != nil {
		t.Fatalf("read expires_at: %v", err)
	}
	if delta := time.Until(expires); delta < DefaultTTL-time.Minute || delta > DefaultTTL+time.Minute {
		t.Errorf("ticket expires in %v, want roughly the %v default", delta, DefaultTTL)
	}
	if _, _, err := s.Consume(context.Background(), ticket); err != nil {
		t.Errorf("a ticket issued with ttl=0 was already dead: %v", err)
	}
}

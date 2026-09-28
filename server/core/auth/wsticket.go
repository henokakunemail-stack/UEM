package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/wsticket"
)

// WebSocketTicketIssuer mints the one-time tickets a browser redeems on a
// WebSocket handshake. The two handshake handlers take this as an interface
// rather than a *wsticket.Store so they can be built in a test without a
// database, and so this package does not have to expose its store internals.
type WebSocketTicketIssuer interface {
	Issue(ctx context.Context, subject, purpose string, ttl time.Duration) (string, error)
	ConsumeFor(ctx context.Context, ticket, purpose string) (subject string, err error)
}

// ticketIssuerFor is the process wiring: the LoginHandler builds the store (it
// already holds the database) and main.go hands that store to the two modules
// whose handshakes need it. It is a package variable rather than a field on
// every handler because threading a ninth constructor argument through two
// modules -- one of which has a fixed signature used by an integration test --
// is a larger change than the indirection costs, and the assignment happens
// once during startup before the listener is serving.
//
// A nil value is the un-wired state and every redeem refuses, which is the safe
// direction: an unconfigured handshake fails closed rather than falling back to
// accepting whatever arrives.
var (
	ticketMu     sync.RWMutex
	ticketIssuer WebSocketTicketIssuer
	ticketRoles  RoleReader
)

// SetWebSocketTicketIssuer installs the store the WebSocket handshakes redeem
// against, and the reader that resolves what the named user may currently do.
// Both are set from the composition root before the listener starts serving.
//
// They are package state rather than constructor arguments because the two
// handshake handlers take a *auth.JWTService and a middleware and nothing else,
// and that signature is fixed by an integration test as well as by main.go.
// Widening it to carry a database into packages that only need to redeem one
// string and read one role is a larger change than a single assignment made
// once during startup.
func SetWebSocketTicketIssuer(s WebSocketTicketIssuer, roles RoleReader) {
	ticketMu.Lock()
	defer ticketMu.Unlock()
	ticketIssuer = s
	ticketRoles = roles
}

func currentTicketIssuer() WebSocketTicketIssuer {
	ticketMu.RLock()
	defer ticketMu.RUnlock()
	return ticketIssuer
}

func currentRoleReader() RoleReader {
	ticketMu.RLock()
	defer ticketMu.RUnlock()
	return ticketRoles
}

// CurrentRoleReader exposes the wired reader so a WebSocket handler can turn a
// redeemed ticket into claims without taking its own database handle.
func CurrentRoleReader() RoleReader { return currentRoleReader() }

// The purposes a console client may ask for. Re-exported here so a WebSocket
// handler names the purpose it redeems against through this package rather than
// importing the store directly: the minting side and the redeeming side have to
// agree on the spelling exactly, and a mismatch fails closed and looks to the
// user like a lost login.
const (
	// PurposeRemoteExec is the interactive terminal socket.
	PurposeRemoteExec = wsticket.PurposeRemoteExec
	// PurposeRemoteDesktop is the remote control socket.
	PurposeRemoteDesktop = wsticket.PurposeRemoteDesktop
)

// TicketFromRequest pulls a ticket out of a handshake URL. The parameter is
// named "ticket", not "token", so a stale client still sending ?token=<jwt> is
// visibly not a ticket rather than being silently reinterpreted as one.
func TicketFromRequest(r *http.Request) string {
	return r.URL.Query().Get("ticket")
}

// RoleReader resolves a user's current identity and role.
//
// A ticket names a user, not a set of privileges, so the consuming side has to
// ask what that user may do now. A role carried by the ticket would be a second
// copy of an authorisation decision with its own expiry, which is exactly the
// stale-privilege problem the refresh path had to be fixed for.
type RoleReader interface {
	RoleFor(ctx context.Context, userID string) (username, role string, err error)
}

// ClaimsForUser builds the claims a ticket redeems to, reading the live user row
// rather than anything the ticket carried. A deactivated or credential-stripped
// account is refused here even though its ticket is still within its TTL,
// because the ticket outliving the account that asked for it is the one case a
// TTL alone does not cover.
func ClaimsForUser(ctx context.Context, roles RoleReader, userID string) (Claims, bool) {
	if roles == nil || userID == "" {
		return Claims{}, false
	}
	username, role, err := roles.RoleFor(ctx, userID)
	if err != nil || role == "" {
		return Claims{}, false
	}
	return Claims{UserID: userID, Username: username, Role: role}, true
}

// RedeemWebSocketTicket spends a ticket and returns the user id it was minted
// for, or ok=false for a ticket that is unknown, expired, already spent, minted
// for a different purpose, or that arrived before the store was wired.
//
// Those cases are deliberately not distinguished. A caller able to tell
// "expired" from "wrong purpose" could use the handshake as an oracle to test
// whether a value it found in a log is still live, and the ticket is the only
// secret in the exchange.
func RedeemWebSocketTicket(ctx context.Context, ticket, purpose string) (userID string, ok bool) {
	s := currentTicketIssuer()
	if s == nil {
		return "", false
	}
	sub, err := s.ConsumeFor(ctx, ticket, purpose)
	if err != nil {
		return "", false
	}
	return sub, true
}

// ErrNoTicketIssuer is returned by the mint path when the store is not wired.
// It is not reachable in the running server, where main.go sets the issuer
// before the listener starts; it exists so a unit test that builds a
// LoginHandler in isolation gets a clear reason rather than a 401 it cannot
// explain.
var ErrNoTicketIssuer = errors.New("web socket ticket issuer is not configured")

// issueWSTicket mints a one-time ticket a browser can redeem on a WebSocket
// handshake.
//
// The handshake is the reason this endpoint exists. A browser cannot attach an
// Authorization header to a WebSocket, so the credential used to open a socket
// has always travelled in the query string -- and a query string is written to
// the access log, kept in history, and forwarded in Referer. A JWT captured
// there is a working credential for its whole TTL with no row to revoke.
//
// The ticket is the replacement: it is fetched over an ordinary authenticated
// request, so the JWT never appears in a URL, and it is destroyed by the
// handshake that spends it. What reaches the log is already worthless.
func (h *LoginHandler) issueWSTicket(w http.ResponseWriter, r *http.Request) {
	purpose := r.URL.Query().Get("purpose")
	// Taken from a closed set rather than stored verbatim: the value ends up in
	// the consuming WHERE clause, and an open value would let a caller mint a
	// ticket naming a purpose the server does not implement.
	switch purpose {
	case wsticket.PurposeRemoteExec, wsticket.PurposeRemoteDesktop:
	default:
		writeErr(w, http.StatusBadRequest, "unknown purpose")
		return
	}

	issuer := currentTicketIssuer()
	if issuer == nil {
		writeErr(w, http.StatusServiceUnavailable, "tickets unavailable")
		return
	}
	ticket, err := issuer.Issue(r.Context(), UserIDFromContext(r.Context()), purpose, wsticket.DefaultTTL)
	if err != nil {
		if errors.Is(err, wsticket.ErrStoreFull) {
			// Server state, not a bad request, and the caller can usefully retry.
			writeErr(w, http.StatusServiceUnavailable, "too many outstanding tickets, retry shortly")
			return
		}
		log.Error().Err(err).Msg("issue ws ticket")
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"ticket":  ticket,
		"purpose": purpose,
	})
}

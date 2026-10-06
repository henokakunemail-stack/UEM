package directory

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// directoryClient is the one interface in this package, and it earns its place
// for a single reason: without it, testing diffContacts needs a live AD server.
// With it, the diff — which is where every real bug in this feature lives —
// is tested against literal slices.
type directoryClient interface {
	Search(ctx context.Context, cfg Config) ([]Contact, error)
	// Test performs the bind and one search, returning how many entries the
	// filter matched. It exists so an operator can find out their base DN is
	// wrong before clicking Preview and reading "0 adds" as good news.
	Test(ctx context.Context, cfg Config) (int, error)
}

// ldapClient is the production implementation. Pure-Go, so it needs no CGO and
// builds for the same five targets as the rest of the server.
type ldapClient struct {
	// dialTimeout bounds every network wait. An operator who types a host that
	// black-holes packets should get an error in seconds, not a spinner that
	// outlives their patience.
	dialTimeout time.Duration
}

func newLDAPClient() *ldapClient { return &ldapClient{dialTimeout: 10 * time.Second} }

// NewLDAPClient is the exported constructor, so main.go does not reach past the
// package boundary for the dial timeout.
func NewLDAPClient() directoryClient { return newLDAPClient() }

const (
	attrEntryUUID  = "entryUUID"  // RFC 4519; AD populates it with the objectGUID value
	attrObjectGUID = "objectGUID" // AD, binary
	attrDN         = "distinguishedName"
	attrDisplay    = "displayName"
	attrName       = "cn"
	attrMail       = "mail"
	attrDept       = "department"
	attrTitle      = "title"
	attrSAM        = "sAMAccountName" // AD
	attrUID        = "uid"            // plain LDAP
)

// requestedAttrs is what every search asks for. Naming the attributes rather
// than asking for "*" is not an optimisation: on a large AD, "*" pulls memberOf
// and tokenGroups and can return megabytes per entry for values this server
// discards. sAMAccountName is present on AD and uid on OpenLDAP; whichever is
// absent simply comes back missing, which is harmless.
var requestedAttrs = []string{
	attrEntryUUID, attrObjectGUID, attrDN, attrDisplay, attrName,
	attrMail, attrDept, attrTitle, attrSAM, attrUID,
}

func (c *ldapClient) Search(ctx context.Context, cfg Config) ([]Contact, error) {
	contacts, _, err := c.run(ctx, cfg)
	return contacts, err
}

func (c *ldapClient) Test(ctx context.Context, cfg Config) (int, error) {
	_, n, err := c.run(ctx, cfg)
	return n, err
}

func (c *ldapClient) run(ctx context.Context, cfg Config) ([]Contact, int, error) {
	// go-ldap's Search takes no context, so the cancellation check happens
	// here instead of inside the call. Enough to stop a sync the operator
	// navigated away from; it does not abort a search already in flight.
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	l, err := c.dial(cfg)
	if err != nil {
		return nil, 0, err
	}
	defer l.Close()

	req := ldap.NewSearchRequest(
		cfg.BaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, 0, false,
		searchFilter(cfg.SearchFilter),
		requestedAttrs,
		nil,
	)

	// Paged rather than one unbounded request: an unbounded search against a
	// whole-company base DN either trips the server's size limit or returns
	// far more than fits in memory, and the error for that is opaque.
	result, err := l.SearchWithPaging(req, 500)
	if err != nil {
		return nil, 0, fmt.Errorf("search directory: %w", err)
	}

	contacts := make([]Contact, 0, len(result.Entries))
	for _, e := range result.Entries {
		c := contactFromEntry(cfg, e)
		// An entry with no name cannot be chosen from a dropdown, so it is
		// dropped rather than stored as a blank row.
		if c.DisplayName == "" {
			continue
		}
		contacts = append(contacts, c)
	}
	return contacts, len(result.Entries), nil
}

// dial opens a connection and binds. The bind is deliberately separate from the
// connect: reaching an LDAP port says nothing about whether the credentials
// work, and an operator who got both wrong should not have to work out which.
func (c *ldapClient) dial(cfg Config) (*ldap.Conn, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("directory host is not set")
	}
	if strings.TrimSpace(cfg.BaseDN) == "" {
		return nil, fmt.Errorf("directory base DN is not set")
	}

	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))

	// Two TLS shapes, told apart by the port rather than by a separate flag:
	// 636 is implicit TLS (LDAPS), 389 with use_tls is StartTLS. Both then
	// carry the same credentials, so getting it wrong means either a
	// connection reset or a plaintext bind with a password in it. Hence no
	// silent "try secure, fall back to plain".
	var (
		l   *ldap.Conn
		err error
	)
	if cfg.Port == 636 {
		l, err = ldap.DialURL("ldaps://"+addr, ldap.DialWithTLSConfig(&tls.Config{
			ServerName: cfg.Host,
			MinVersion: tls.VersionTLS12,
		}))
	} else {
		l, err = ldap.DialURL("ldap://"+addr, ldap.DialWithDialer(&net.Dialer{Timeout: c.dialTimeout}))
	}
	if err != nil {
		return nil, fmt.Errorf("connect to directory %s: %w", addr, err)
	}
	l.SetTimeout(c.dialTimeout)

	if cfg.UseTLS && cfg.Port != 636 {
		if err := l.StartTLS(&tls.Config{
			ServerName: cfg.Host,
			MinVersion: tls.VersionTLS12,
		}); err != nil {
			l.Close()
			return nil, fmt.Errorf("start TLS on %s: %w", addr, err)
		}
	}

	// No bind_dn means an anonymous search, which some directories allow and
	// most do not. Either way the bind is attempted only when there is an
	// account to bind as, and the error names the account so the operator can
	// tell a bad password from a bad base DN.
	if strings.TrimSpace(cfg.BindDN) != "" {
		if err := l.Bind(cfg.BindDN, cfg.BindPassword); err != nil {
			l.Close()
			return nil, fmt.Errorf("bind as %s: %w", cfg.BindDN, err)
		}
	}
	return l, nil
}

// searchFilter falls back to the default only when the operator left the field
// blank. A filter they typed is used verbatim: second-guessing it would be
// worse than the wrong filter being visibly wrong.
func searchFilter(f string) string {
	if f = strings.TrimSpace(f); f != "" {
		return f
	}
	return "(objectClass=person)"
}

func contactFromEntry(cfg Config, e *ldap.Entry) Contact {
	return Contact{
		ExternalID:        stableExternalID(e),
		DistinguishedName: e.GetAttributeValue(attrDN),
		DisplayName:       firstNonEmpty(e.GetAttributeValue(attrDisplay), e.GetAttributeValue(attrName)),
		Email:             e.GetAttributeValue(attrMail),
		Department:        e.GetAttributeValue(attrDept),
		Title:             e.GetAttributeValue(attrTitle),
		Username:          firstNonEmpty(e.GetAttributeValue(attrSAM), e.GetAttributeValue(attrUID)),
		Source:            cfg.Source,
		IsActive:          true,
	}
}

// stableExternalID picks the identifier that survives an OU move.
//
// entryUUID first: it is the standard, and AD populates it with the same value
// as objectGUID. objectGUID is binary, so it is hex-encoded — without that the
// raw bytes land in a TEXT column as replacement characters and two different
// people collide on the UNIQUE index. The DN is the last resort because it
// changes whenever somebody moves between OUs, which would re-create the entire
// directory as new contacts on every reorg.
func stableExternalID(e *ldap.Entry) string {
	if v := strings.TrimSpace(e.GetAttributeValue(attrEntryUUID)); v != "" {
		return v
	}
	if raw := e.GetRawAttributeValue(attrObjectGUID); len(raw) > 0 {
		return fmt.Sprintf("%x", raw)
	}
	return e.DN
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

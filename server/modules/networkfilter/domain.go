package networkfilter

import (
	"net"
	"strings"
)

// Reason codes a rejected pattern carries back to the console, which translates
// them; the console never parses the English message.
const (
	ReasonEmpty          = "empty"
	ReasonInvalidChars   = "invalid_chars"
	ReasonIPLiteral      = "ip_literal"
	ReasonTooLong        = "too_long"
	ReasonNoTLD          = "no_tld"
	ReasonIDN            = "idn_not_supported"
	ReasonDuplicate      = "duplicate_existing"
	ReasonDuplicateBatch = "duplicate_in_batch"
)

// NormalizeDomainPattern turns what an operator typed or pasted into the domain
// the agent will actually enforce, or says why it cannot be one. Patterns used
// to be stored verbatim: "https://Example.com/login" was saved as typed, shown
// in the console as a rule, and the agent quietly reduced it to example.com --
// while "example" or "10.0.0.1" were saved too and enforced nothing.
//
// The reduction mirrors the agent's normalizeDomain (scheme, "*." prefix, port
// and path dropped, lower case, no trailing dot). "*.example.com" becomes
// "example.com": the whitelist matcher includes subdomains of a bare domain,
// and the blacklist hosts file cannot express a wildcard either way.
func NormalizeDomainPattern(in string) (string, string) {
	d := strings.TrimSpace(in)
	lower := strings.ToLower(d)
	for _, scheme := range []string{"https://", "http://"} {
		if strings.HasPrefix(lower, scheme) {
			d = d[len(scheme):]
			break
		}
	}
	d = strings.TrimPrefix(d, "*.")
	if strings.HasPrefix(d, "[") {
		// A bracketed IPv6 literal, with or without a port.
		return "", ReasonIPLiteral
	}
	if i := strings.IndexAny(d, "/:?#"); i >= 0 {
		d = d[:i]
	}
	d = strings.ToLower(strings.TrimSuffix(d, "."))

	if d == "" {
		return "", ReasonEmpty
	}
	for i := 0; i < len(d); i++ {
		if d[i] >= 0x80 {
			return "", ReasonIDN
		}
	}
	if net.ParseIP(d) != nil {
		return "", ReasonIPLiteral
	}
	for i := 0; i < len(d); i++ {
		c := d[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			return "", ReasonInvalidChars
		}
	}
	if len(d) > 253 {
		return "", ReasonTooLong
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return "", ReasonNoTLD
	}
	for _, l := range labels {
		if l == "" || l[0] == '-' || l[len(l)-1] == '-' {
			return "", ReasonInvalidChars
		}
		if len(l) > 63 {
			return "", ReasonTooLong
		}
	}
	// An all-numeric last label is an address written with fewer than four
	// parts ("10.1.2"), not a name under a top-level domain.
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", ReasonIPLiteral
	}
	return d, ""
}

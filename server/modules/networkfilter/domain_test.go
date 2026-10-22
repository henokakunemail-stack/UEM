package networkfilter

import (
	"strings"
	"testing"
)

func TestNormalizeDomainPattern(t *testing.T) {
	cases := []struct {
		in, want, reason string
	}{
		{"example.com", "example.com", ""},
		{"  Example.COM.  ", "example.com", ""},
		{"https://www.Example.com/login?x=1", "www.example.com", ""},
		{"HTTP://example.com:8080", "example.com", ""},
		{"*.tiktok.com", "tiktok.com", ""},
		{"sub_domain.example.co.id", "sub_domain.example.co.id", ""},
		{"", "", ReasonEmpty},
		{"   ", "", ReasonEmpty},
		{"https://", "", ReasonEmpty},
		{"example", "", ReasonNoTLD},
		{"localhost", "", ReasonNoTLD},
		{"10.0.0.1", "", ReasonIPLiteral},
		{"10.0.2", "", ReasonIPLiteral},
		{"[2001:db8::1]:443", "", ReasonIPLiteral},
		{"exa mple.com", "", ReasonInvalidChars},
		{"-bad.com", "", ReasonInvalidChars},
		{"bad-.com", "", ReasonInvalidChars},
		{"a..b.com", "", ReasonInvalidChars},
		{"bücher.de", "", ReasonIDN},
		{strings.Repeat("a", 64) + ".com", "", ReasonTooLong},
		{strings.Repeat("abcdefghi.", 26) + "com", "", ReasonTooLong},
	}
	for _, c := range cases {
		got, reason := NormalizeDomainPattern(c.in)
		if got != c.want || reason != c.reason {
			t.Errorf("NormalizeDomainPattern(%q) = %q, %q; want %q, %q", c.in, got, reason, c.want, c.reason)
		}
	}
}

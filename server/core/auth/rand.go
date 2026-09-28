package auth

import (
	"crypto/rand"
	"encoding/base64"
)

// newJTI returns the unique id that makes one refresh token distinguishable
// from every other. It is a session identifier, not a secret: it appears in
// cleartext in the token payload and is only ever compared against a row the
// server already holds. That is deliberate -- a secret here would add nothing,
// because possession of the signed token is already the credential.
func newJTI() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// Raw URL encoding, not hex: this value also becomes part of a URL in the
	// console's session-management links, and base64's alphabet is shorter.
	return base64.RawURLEncoding.EncodeToString(b), nil
}

package protocol

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// ReleaseManifest is the signed statement of what an update artifact is. It is
// declared here, in the shared protocol package, because it is verified on the
// far side of a binary boundary: the server records it, an agent built months
// later and cross-compiled for another platform has to accept or reject it.
//
// Keeping it in the agent package alone would mean the server's signer and the
// agent's verifier each had their own idea of the field set, and a field added
// on one side would compile cleanly on both and change the signed bytes on only
// one -- every signature in the fleet silently invalid, with the failure
// surfacing as "agent refuses to update" and no indication why.
type ReleaseManifest struct {
	Version                 string `json:"version"`
	OSName                  string `json:"os_name"`
	Arch                    string `json:"arch"`
	SHA256Checksum          string `json:"sha256"`
	Size                    int64  `json:"size"`
	URL                     string `json:"url"`
	PublishedAt             string `json:"published_at"`
	MinimumSupportedVersion string `json:"minimum_supported_version"`
}

// SignedManifest is the manifest plus its detached Ed25519 signature.
type SignedManifest struct {
	Manifest  ReleaseManifest `json:"manifest"`
	Signature string          `json:"signature"` // base64, 64 raw bytes
}

var (
	// ErrNoSignature means the release has no signature on it: it was uploaded
	// before signing existed, or by a path that never obtained one. The caller
	// decides policy; a signature check that failed here would brick every
	// fleet still carrying queued unsigned releases.
	ErrNoSignature  = errors.New("update manifest is not signed")
	ErrBadSignature = errors.New("update manifest signature is invalid")
)

// SigningBytes serialises the manifest for signing and verifying. Both sides
// of the boundary must produce byte-identical output or a signature made by
// one is meaningless to the other.
//
// A struct (not a map) fixes the field set, so a new field is a compile error
// on whichever side has not added it rather than a silent change to what is
// signed. json.Marshal emits struct fields in declaration order, so the
// ordering is fixed by this file, and the encoding is stable across Go
// versions because the order is not derived from a map.
func (m ReleaseManifest) SigningBytes() ([]byte, error) {
	return json.Marshal(m)
}

// Sign returns the base64 signature a signing host records against the release.
// The private key stays on that host; the server that serves the artifact never
// holds it, which is what makes a signature worth more than the checksum it
// accompanies.
func (m ReleaseManifest) Sign(privateKey ed25519.PrivateKey) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("sign manifest: key is %d bytes, want %d", len(privateKey), ed25519.PrivateKeySize)
	}
	b, err := m.SigningBytes()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, b)), nil
}

// Verify checks the manifest against a trusted public key. Anything that cannot
// verify -- including a signature that is not valid base64 or not exactly 64
// bytes -- returns ErrBadSignature, so a malformed signature and a wrong one
// are indistinguishable to the caller.
func (m ReleaseManifest) Verify(publicKey ed25519.PublicKey, signature string) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("verify manifest: key is %d bytes, want %d", len(publicKey), ed25519.PublicKeySize)
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return ErrBadSignature
	}
	if len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	b, err := m.SigningBytes()
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, b, sig) {
		return ErrBadSignature
	}
	return nil
}

package update

import (
	"crypto/ed25519"
	"runtime"
	"testing"
)

// Release rows uploaded before the server normalised os_name say "darwin" for
// macOS, and the signature covers that stored value. The agent compared it to
// "macos" literally, so a correctly signed macOS release was refused as a
// platform mismatch.
func TestCanonicalOSNameFoldsDarwinIntoMacOS(t *testing.T) {
	for in, want := range map[string]string{
		"darwin": "macos", "macos": "macos", "Darwin": "macos",
		"windows": "windows", "linux": "linux",
	} {
		if got := canonicalOSName(in); got != want {
			t.Errorf("canonicalOSName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A manifest naming this platform by its GOOS spelling verifies, which on a
// Mac is exactly the legacy "darwin" row.
func TestAManifestNamingThePlatformByGOOSVerifies(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := ReleaseManifest{
		Version: "2.0.0", OSName: runtime.GOOS, Arch: releaseArch(),
		SHA256Checksum: "abc", Size: 3, URL: "/x", PublishedAt: "2026-10-22T00:00:00Z",
	}
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine("http://127.0.0.1:1", "dev", "secret").WithSigningKey(pub, "1.0.0")
	if err := e.verifyManifest(SignedManifest{Manifest: m, Signature: sig},
		UpdateParams{SHA256Checksum: "abc", FileSize: 3}); err != nil {
		t.Fatalf("manifest for %s/%s refused: %v", runtime.GOOS, releaseArch(), err)
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A restart that regenerates the signing secret logs every operator out and
// invalidates every refresh token, so the secret has to survive on disk. This
// asserts the file keeps it and a second load reads back the same value.
func TestEnsureJWTSecretWritesAndThenKeepsTheSameSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.env")
	body := "HTTP_ADDR=:8443\nJWT_SECRET=\nDB_PATH=/var/lib/em/server.db\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JWT_SECRET", "")
	os.Unsetenv("JWT_SECRET")

	if err := ensureJWTSecret(path); err != nil {
		t.Fatalf("ensureJWTSecret: %v", err)
	}
	first := os.Getenv("JWT_SECRET")
	if len(first) != 64 {
		t.Fatalf("secret is %d hex chars, want 64 (32 bytes)", len(first))
	}
	if first == "" {
		t.Fatal("secret was not applied to the environment")
	}

	// The rest of the file must be intact: clobbering DB_PATH here would point
	// the next start at a different database than this one just used.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "DB_PATH=/var/lib/em/server.db") {
		t.Errorf("rewriting the secret lost the other settings:\n%s", got)
	}
	if !strings.Contains(got, "HTTP_ADDR=:8443") {
		t.Errorf("rewriting the secret lost the other settings:\n%s", got)
	}

	// Restart: a fresh process, same file. The value must be adopted, not
	// replaced.
	t.Setenv("JWT_SECRET", "")
	os.Unsetenv("JWT_SECRET")
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if err := ensureJWTSecret(path); err != nil {
		t.Fatal(err)
	}
	if second := os.Getenv("JWT_SECRET"); second != first {
		t.Errorf("secret changed on restart: %q -> %q", first, second)
	}
}

// An operator who exports their own secret means it, and the file must not
// overwrite it -- least of all by rewriting the file with a new value.
func TestEnsureJWTSecretLeavesAnOperatorSuppliedSecretAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.env")
	if err := os.WriteFile(path, []byte("JWT_SECRET=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JWT_SECRET", "operator-chose-this")

	if err := ensureJWTSecret(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("JWT_SECRET"); got != "operator-chose-this" {
		t.Errorf("JWT_SECRET = %q, want the operator's value", got)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "operator-chose-this") {
		t.Error("the operator's secret was written into the env file")
	}
}

// A truncated secret would start, sign tokens, and then fail to verify any of
// them on the next boot. The temp-file rename is what prevents that, so assert
// the temp file is not left behind on the success path either.
func TestEnsureJWTSecretLeavesNoTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.env")
	if err := os.WriteFile(path, []byte("JWT_SECRET=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JWT_SECRET", "")
	os.Unsetenv("JWT_SECRET")

	if err := ensureJWTSecret(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("the temp file used for the atomic write was left behind")
	}
}

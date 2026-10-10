package config

import (
	"strings"
	"testing"
)

func TestConfigLoad_JWTSecretValidation(t *testing.T) {
	// Case 1: Empty JWT_SECRET
	t.Setenv("JWT_SECRET", "")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET must be set") {
		t.Fatalf("expected error for empty JWT_SECRET, got: %v", err)
	}

	// Case 2: Short JWT_SECRET (< 32 chars)
	t.Setenv("JWT_SECRET", "too-short-secret-123")
	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "at least 32 characters") {
		t.Fatalf("expected error for short JWT_SECRET, got: %v", err)
	}

	// Case 3: Valid JWT_SECRET (>= 32 chars)
	t.Setenv("JWT_SECRET", "super-secret-key-that-is-at-least-32-bytes-long!")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error for valid JWT_SECRET: %v", err)
	}
	if cfg.JWTSecret != "super-secret-key-that-is-at-least-32-bytes-long!" {
		t.Errorf("got JWTSecret %q, want expected", cfg.JWTSecret)
	}
}

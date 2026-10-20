package securityaudit

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/config"
)

func TestEvaluatorAllChecks(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite memory: %v", err)
	}
	defer db.Close()

	// Initialize basic schema needed by evaluator
	_, err = db.Exec(`
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL,
			password_hash TEXT NOT NULL
		);
		CREATE TABLE auth_sessions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			token_hash TEXT NOT NULL,
			expires_at TIMESTAMP NOT NULL
		);
		CREATE TABLE audit_logs (
			id TEXT PRIMARY KEY,
			timestamp TIMESTAMP NOT NULL
		);
		CREATE TABLE software_licenses (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL
		);
	`)
	if err != nil {
		t.Fatalf("create test schema: %v", err)
	}

	cfg := &config.Config{
		JWTSecret:            "test-secret-that-is-at-least-32-bytes-long-super-secure!",
		LogLevel:             "info",
		AllowedOriginDomains: []string{"localhost"},
	}

	evaluator := NewEvaluator(cfg, db)
	report := evaluator.Evaluate(context.Background())

	if report.TotalChecks != 40 {
		t.Fatalf("expected 40 checks, got %d", report.TotalChecks)
	}
	if len(report.Checks) != 40 {
		t.Fatalf("expected 40 check items, got %d", len(report.Checks))
	}
	if report.Passed < 30 {
		t.Errorf("expected at least 30 passed checks in default configuration, got %d", report.Passed)
	}
	if report.Score < 70 {
		t.Errorf("expected score >= 70, got %d", report.Score)
	}

	// Verify both parts exist
	part1Count, part2Count := 0, 0
	for _, c := range report.Checks {
		switch c.Part {
		case 1:
			part1Count++
		case 2:
			part2Count++
		}
	}
	if part1Count != 20 {
		t.Errorf("expected 20 checks in Part 1, got %d", part1Count)
	}
	if part2Count != 20 {
		t.Errorf("expected 20 checks in Part 2, got %d", part2Count)
	}
}

package db

import (
	"database/sql"
	"os"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestRebindDriver_DirectQuestionMarkQuery(t *testing.T) {
	pgURL := os.Getenv("UEM_TEST_POSTGRES_URL")
	if pgURL == "" {
		t.Skip("UEM_TEST_POSTGRES_URL not set; skipping live postgres rebind test")
	}

	d, err := sqlx.Open("postgres-rebind", pgURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if err := d.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// Raw ? placeholder query that would normally fail on lib/pq without Rebind
	var result int
	err = d.Get(&result, "SELECT ?::int + ?::int", 10, 20)
	if err != nil {
		t.Fatalf("query with raw '?' failed on postgres-rebind: %v", err)
	}
	if result != 30 {
		t.Fatalf("expected 30, got %d", result)
	}

	// Exec with raw '?'
	_, err = d.Exec("SELECT ?::text", "hello")
	if err != nil {
		t.Fatalf("exec with raw '?' failed on postgres-rebind: %v", err)
	}

	// Prepared statement with raw '?'
	stmt, err := d.Prepare("SELECT ?::int * 2")
	if err != nil {
		t.Fatalf("prepare with raw '?' failed: %v", err)
	}
	defer stmt.Close()

	var doubled int
	if err := stmt.QueryRow(5).Scan(&doubled); err != nil {
		t.Fatalf("stmt query failed: %v", err)
	}
	if doubled != 10 {
		t.Fatalf("expected 10, got %d", doubled)
	}
}

func TestRebindDriver_QueryWithoutQuestionMarks(t *testing.T) {
	pgURL := os.Getenv("UEM_TEST_POSTGRES_URL")
	if pgURL == "" {
		t.Skip("UEM_TEST_POSTGRES_URL not set; skipping live postgres rebind test")
	}

	d, err := sqlx.Open("postgres-rebind", pgURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	var val int
	if err := d.Get(&val, "SELECT 42"); err != nil {
		t.Fatalf("SELECT 42 failed: %v", err)
	}
	if val != 42 {
		t.Fatalf("expected 42, got %d", val)
	}
}

// Ensure interface compatibility
var _ sql.Scanner

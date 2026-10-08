package db

import (
	"io/fs"
	"strings"
	"testing"
)

// TestDialectFor_DefaultsToSQLite keeps the standalone deployment untouched: an
// unrecognised driver name must not silently become anything else.
func TestDialectFor_DefaultsToSQLite(t *testing.T) {
	for _, name := range []string{"", "sqlite", "sqlite3", "typo", "mysql"} {
		if d := dialectFor(name); d.postgres {
			t.Errorf("dialectFor(%q).postgres = true, want false", name)
		}
	}
	for _, name := range []string{"postgres", "PostgreSQL"} {
		if d := dialectFor(name); !d.postgres {
			t.Errorf("dialectFor(%q).postgres = false, want true", name)
		}
	}
}

// TestDialect_SQLiteScriptsPassThroughUnchanged proves the translation is inert
// on the default path. A dialect that rewrote the SQLite scripts would be a far
// larger risk than the one it is meant to fix.
func TestDialect_SQLiteScriptsPassThroughUnchanged(t *testing.T) {
	d := dialectFor("sqlite")
	names := migrationNames(t)
	for _, name := range names {
		raw := readMigration(t, name)
		if got := d.translate(raw); got != raw {
			t.Errorf("%s was rewritten on the sqlite path", name)
		}
	}
}

// TestDialect_PostgresTranslation checks the three confirmed differences.
func TestDialect_PostgresTranslation(t *testing.T) {
	d := dialectFor("postgres")
	cases := []struct{ name, in, want string }{
		{"datetime type", "created_at DATETIME NOT NULL", "created_at TIMESTAMPTZ NOT NULL"},
		{"word boundary", "my_datetime_column TEXT", "my_datetime_column TEXT"},
		{
			"insert or ignore",
			"INSERT OR IGNORE INTO script_templates (id, name) VALUES (?, ?);",
			"INSERT INTO script_templates (id, name) VALUES (?, ?) ON CONFLICT DO NOTHING;",
		},
		{
			// The real shape in 0018: both the column list and the value list
			// span many lines, and the conflict clause belongs after the whole
			// value list, not at the INSERT keyword where SQLite puts it.
			"multiline insert",
			"INSERT OR IGNORE INTO script_templates (\n    id, name,\n    created_at\n) VALUES (\n    'x', 'y', ?\n);",
			"INSERT INTO script_templates (\n    id, name,\n    created_at\n) VALUES (\n    'x', 'y', ?\n) ON CONFLICT DO NOTHING;",
		},
	}
	for _, c := range cases {
		if got := d.translate(c.in); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestDialect_NoSQLiteSyntaxSurvivesTranslation is the guard that matters: a
// script containing a construct PostgreSQL rejects must not reach the server
// unchanged. DATETIME and INSERT OR IGNORE both fail there, and the errors read
// like a connectivity problem rather than a syntax one, so a silent miss here
// is expensive to diagnose.
//
// Only executable lines are checked. 0018 documents the INSERT OR IGNORE idiom
// in a comment, and PostgreSQL ignores a keyword inside a comment exactly as
// SQLite does -- rewriting the prose would corrupt the explanation while fixing
// nothing.
func TestDialect_NoSQLiteSyntaxSurvivesTranslation(t *testing.T) {
	d := dialectFor("postgres")
	for _, name := range migrationNames(t) {
		for i, line := range strings.Split(d.translate(readMigration(t, name)), "\n") {
			code := strings.TrimSpace(line)
			if code == "" || strings.HasPrefix(code, "--") {
				continue
			}
			if strings.Contains(code, "DATETIME") {
				t.Errorf("%s:%d still has DATETIME: %s", name, i+1, code)
			}
			if strings.Contains(strings.ToUpper(code), "OR IGNORE") {
				t.Errorf("%s:%d still has OR IGNORE: %s", name, i+1, code)
			}
		}
	}
}

// TestDialect_PlaceholderIsNotRewrittenHere documents the division of labour:
// '?' survives translation and is converted at bind time by sqlx, which is what
// keeps 83 call sites out of this change. PostgreSQL treats a literal '?' as a
// value, so the conversion has to actually happen later -- it is covered by the
// bind-type registration test.
func TestDialect_PlaceholderIsNotRewrittenHere(t *testing.T) {
	d := dialectFor("postgres")
	in := "SELECT * FROM devices WHERE id = ? AND name = ?"
	if got := d.translate(in); got != in {
		t.Errorf("translate rewrote bind placeholders: %q", got)
	}
}

func migrationNames(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatal("no migrations found")
	}
	return names
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(migrations, "migrations/"+name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

package dbmove

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

func scratchPostgres(t *testing.T) string {
	t.Helper()
	base := os.Getenv("UEM_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("UEM_TEST_POSTGRES_URL not set")
	}
	admin, err := sqlx.Connect("postgres", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("uem_move_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c, err := sqlx.Connect("postgres", base); err == nil {
			_, _ = c.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
			c.Close()
		}
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	return u.String()
}

// legacyChain appends audit rows the way a pre-0027 SQLite server did: hashed
// over a nanosecond timestamp, which PostgreSQL cannot store. These are the
// rows that report as tampered after a naive copy.
func legacyChain(t *testing.T, d *sqlx.DB, n int) {
	t.Helper()
	prev := ""
	var maxSeq int64
	_ = d.Get(&maxSeq, `SELECT COALESCE(MAX(seq), 0) FROM audit_logs`)
	if err := d.Get(&prev, `SELECT entry_hash FROM audit_logs WHERE entry_hash <> '' ORDER BY created_at DESC, seq DESC LIMIT 1`); err != nil {
		prev = "0000000000000000000000000000000000000000000000000000000000000000"
	}
	base := time.Now().UTC().Add(time.Hour)
	for i := 0; i < n; i++ {
		e := audit.Entry{
			ID: fmt.Sprintf("legacy-%d", i), ActorType: "user", ActorID: "u-1", Action: "legacy.write",
			TargetID: "t", CreatedAt: base.Add(time.Duration(i)*time.Second + 123456789), PrevHash: prev,
		}
		e.EntryHash = audit.EntryHash(e)
		if _, err := d.Exec(`INSERT INTO audit_logs (id, actor_type, actor_id, action, target_id, created_at, prev_hash, entry_hash, seq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.ID, e.ActorType, e.ActorID, e.Action, e.TargetID, e.CreatedAt, e.PrevHash, e.EntryHash, maxSeq+int64(i)+1); err != nil {
			t.Fatal(err)
		}
		prev = e.EntryHash
	}
}

func seededSQLite(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	now := time.Now().UTC()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, username, password_hash, role, is_active, created_at, updated_at) VALUES ('u-1','admin','hash','admin',1,?,?)`, now, now)
	for i := 0; i < 25; i++ {
		exec(`INSERT INTO devices (id, hostname, os_name, status, enrolled_at, device_secret_hash, created_at, updated_at, site)
			VALUES (?, ?, 'windows', 'offline', ?, 'x', ?, ?, 'HQ')`, fmt.Sprintf("dev-%02d", i), fmt.Sprintf("PC-%02d", i), now, now, now)
	}
	exec(`INSERT INTO alert_rules (id, name, rule_type, threshold_val, severity, created_by, created_at, updated_at)
		VALUES ('r-1','disk','disk_low',10,'warning','u-1',?,?)`, now, now)
	exec(`INSERT INTO alert_incidents (id, rule_id, device_id, severity, title, message, status, trigger_count, first_triggered_at, last_triggered_at)
		VALUES ('i-1','r-1','dev-03','warning','t','m','open',2,?,?)`, now, now)
	for i := 0; i < 5; i++ {
		if err := audit.Log(context.Background(), d, "user", "u-1", "test.current", fmt.Sprint(i), map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	legacyChain(t, d, 5)
	if rep, err := audit.VerifyChain(context.Background(), d); err != nil || !rep.OK {
		t.Fatalf("seeded source chain does not verify: %+v %v", rep, err)
	}
	return path
}

// The whole move: every row arrives, foreign keys hold, the original file is
// untouched, and the audit chain -- including entries hashed at nanosecond
// precision -- still verifies on PostgreSQL.
func TestPostgres_MoveCopiesEverythingAndTheChainStillVerifies(t *testing.T) {
	src := seededSQLite(t)
	before, _ := os.Stat(src)
	dst := scratchPostgres(t)

	res, err := Move(context.Background(), Options{From: src, To: dst, Logf: t.Logf})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if !res.SourceChainOK || !res.AuditChainOK {
		t.Fatalf("audit chain: source ok=%v, target ok=%v", res.SourceChainOK, res.AuditChainOK)
	}
	counts := map[string]int{}
	for _, tr := range res.Tables {
		counts[tr.Table] = tr.Target
	}
	if counts["devices"] != 25 || counts["users"] != 1 || counts["alert_incidents"] != 1 || counts["audit_logs"] < 10 {
		t.Fatalf("row counts after the move: %v", counts)
	}

	after, _ := os.Stat(src)
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatal("the source SQLite file was modified by the move")
	}

	// A second move into the now-populated database is refused without -force.
	if _, err := Move(context.Background(), Options{From: src, To: dst}); err == nil {
		t.Fatal("a move into a database that already holds data was not refused")
	}
	if _, err := Move(context.Background(), Options{From: src, To: dst, Force: true}); err != nil {
		t.Fatalf("forced move: %v", err)
	}
}

// Without the full-precision hash time, a moved legacy chain reports as
// tampered. This proves the hash_time handling is what keeps it valid.
func TestPostgres_LegacyChainBreaksWithoutHashTime(t *testing.T) {
	src := seededSQLite(t)
	dst := scratchPostgres(t)
	if _, err := Move(context.Background(), Options{From: src, To: dst}); err != nil {
		t.Fatal(err)
	}
	pg, err := db.OpenWithDriver("postgres", dst)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if _, err := pg.Exec(`UPDATE audit_logs SET hash_time = NULL WHERE id LIKE 'legacy-%'`); err != nil {
		t.Fatal(err)
	}
	rep, err := audit.VerifyChain(context.Background(), pg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK {
		t.Fatal("legacy nanosecond entries verified without hash_time; the test does not exercise what it claims")
	}
}

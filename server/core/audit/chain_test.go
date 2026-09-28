package audit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// logN writes n entries and returns their ids in chain order.
func logN(t *testing.T, d *sqlx.DB, n int) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		action := fmt.Sprintf("test.action.%d", i)
		if err := Log(ctx, d, "user", "u1", action, "t1", map[string]string{"i": fmt.Sprint(i)}); err != nil {
			t.Fatalf("Log %d: %v", i, err)
		}
		var id string
		if err := d.GetContext(ctx, &id,
			`SELECT id FROM audit_logs WHERE action = ?`, action); err != nil {
			t.Fatalf("read back %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func verify(t *testing.T, d *sqlx.DB) ChainReport {
	t.Helper()
	rep, err := VerifyChain(context.Background(), d)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	return rep
}

func TestChainVerifiesClean(t *testing.T) {
	d := openTestDB(t)
	ids := logN(t, d, 12)

	rep := verify(t, d)
	if !rep.OK {
		t.Fatalf("clean chain reported a break at %s: %s", rep.BreakID, rep.Reason)
	}
	if rep.Checked != 12 {
		t.Errorf("Checked = %d, want 12", rep.Checked)
	}
	if rep.VerifiableFrom != ids[0] {
		t.Errorf("VerifiableFrom = %q, want %q", rep.VerifiableFrom, ids[0])
	}
	if rep.Missing {
		t.Error("clean chain reported a missing row")
	}
}

// The genesis row must be anchored, or deleting the very first entry would
// leave a chain that still verifies.
func TestChainFirstEntryAnchoredToGenesis(t *testing.T) {
	d := openTestDB(t)
	logN(t, d, 3)

	var prev string
	if err := d.GetContext(context.Background(), &prev,
		`SELECT prev_hash FROM audit_logs ORDER BY rowid LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if prev != genesisHash {
		t.Errorf("first prev_hash = %q, want %q", prev, genesisHash)
	}
}

func TestChainDetectsEditedRow(t *testing.T) {
	d := openTestDB(t)
	ids := logN(t, d, 6)
	victim := ids[3]

	// An attacker with write access edits the row and recomputes nothing. The
	// stored entry_hash still covers the old action.
	if _, err := d.Exec(`UPDATE audit_logs SET action = 'test.action.rewritten' WHERE id = ?`, victim); err != nil {
		t.Fatal(err)
	}

	rep := verify(t, d)
	if rep.OK {
		t.Fatal("edited row not detected")
	}
	if rep.Missing {
		t.Error("edit reported as a missing row; the two must be distinguished")
	}
	if rep.BreakID != victim {
		t.Errorf("BreakID = %q, want the edited %q", rep.BreakID, victim)
	}
	if rep.Checked != 4 {
		t.Errorf("Checked = %d, want 4 (walk should stop at the edit)", rep.Checked)
	}
}

// An attacker who knows the scheme can also recompute the edited row's own
// entry_hash, to hide the edit from the "does it recompute" check. The chain
// must still break.
//
// What it cannot do is stay indistinguishable from a deletion. Re-hashing a
// changed row makes it internally consistent, and the only trace left is the
// successor pointing at a hash no row produces any more, which is the same
// trace a deleted row leaves. No comparison of the surviving rows separates
// them, so Missing is reported for both and the reason says "gone or rewritten".
// The important part is that the chain still breaks and names the successor.
func TestChainDetectsEditedRowWithRecomputedOwnHash(t *testing.T) {
	d := openTestDB(t)
	ids := logN(t, d, 5)
	victim, successor := ids[2], ids[3]

	if _, err := d.Exec(`UPDATE audit_logs SET action = 'test.action.rewritten' WHERE id = ?`, victim); err != nil {
		t.Fatal(err)
	}
	var row verifyRow
	if err := d.GetContext(context.Background(), &row,
		`SELECT rowid, id, actor_type, COALESCE(actor_id,'') AS actor_id, action,
		        COALESCE(target_id,'') AS target_id, COALESCE(details,'') AS details,
		        created_at, prev_hash, entry_hash FROM audit_logs WHERE id = ?`, victim); err != nil {
		t.Fatal(err)
	}
	// Forge a self-consistent row: it now hashes to whatever it says.
	forged := entryHashOf(row.entry())
	if _, err := d.Exec(`UPDATE audit_logs SET entry_hash = ? WHERE id = ?`, forged, victim); err != nil {
		t.Fatal(err)
	}

	rep := verify(t, d)
	if rep.OK {
		t.Fatal("a re-hashed forgery was not detected")
	}
	if rep.BreakID != successor {
		t.Errorf("BreakID = %q, want the successor %q that exposes the forgery", rep.BreakID, successor)
	}
	if !rep.Missing {
		t.Errorf("want the dangling-predecessor shape (Missing), got %q", rep.Reason)
	}
	if !strings.Contains(rep.Reason, "rewritten") {
		t.Errorf("reason should say the predecessor was gone or rewritten, got %q", rep.Reason)
	}
}

func TestChainDetectsDeletedRow(t *testing.T) {
	d := openTestDB(t)
	ids := logN(t, d, 6)
	victim := ids[3]

	if _, err := d.Exec(`DELETE FROM audit_logs WHERE id = ?`, victim); err != nil {
		t.Fatal(err)
	}

	rep := verify(t, d)
	if rep.OK {
		t.Fatal("deleted row not detected")
	}
	if !rep.Missing {
		t.Errorf("deletion not reported as missing: %s", rep.Reason)
	}
	// The survivor that noticed is named, because the deleted row cannot be.
	if rep.BreakID != ids[4] {
		t.Errorf("BreakID = %q, want the successor %q", rep.BreakID, ids[4])
	}
}

// Deleting the first linked entry has no grandparent to compare against, the
// case a grandparent-hash heuristic gets wrong.
func TestChainDetectsDeletedFirstLinkedEntry(t *testing.T) {
	d := openTestDB(t)
	ids := logN(t, d, 4)

	if _, err := d.Exec(`DELETE FROM audit_logs WHERE id = ?`, ids[1]); err != nil {
		t.Fatal(err)
	}

	rep := verify(t, d)
	if rep.OK {
		t.Fatal("deletion of the first linked entry not detected")
	}
	if !rep.Missing {
		t.Errorf("not reported as missing: %s", rep.Reason)
	}
}

// Deleting the tail leaves nothing to notice it. The last row still verifies,
// so this is a documented limit rather than a failure to detect.
func TestChainDeletedTailIsNotDetectable(t *testing.T) {
	d := openTestDB(t)
	ids := logN(t, d, 4)

	if _, err := d.Exec(`DELETE FROM audit_logs WHERE id = ?`, ids[3]); err != nil {
		t.Fatal(err)
	}
	rep := verify(t, d)
	if !rep.OK {
		t.Errorf("deleting the tail should still verify, got break at %s: %s", rep.BreakID, rep.Reason)
	}
}

func TestChainPreMigrationRowsAreNotABreak(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Rows as migration 0016 left them: no chain columns.
	old := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		if _, err := d.ExecContext(ctx,
			`INSERT INTO audit_logs (id, actor_type, actor_id, action, target_id, details, created_at)
			 VALUES (?, 'user', 'u0', 'legacy.action', 't1', '{}', ?)`,
			fmt.Sprintf("legacy-%d", i), old.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	ids := logN(t, d, 4)

	rep := verify(t, d)
	if !rep.OK {
		t.Fatalf("pre-migration rows caused a false break at %s: %s", rep.BreakID, rep.Reason)
	}
	if rep.VerifiableFrom != ids[0] {
		t.Errorf("VerifiableFrom = %q, want the first chained row %q", rep.VerifiableFrom, ids[0])
	}
	if rep.Checked != 4 {
		t.Errorf("Checked = %d, want 4; legacy rows must not be counted as chained", rep.Checked)
	}
}

func TestChainEmptyTable(t *testing.T) {
	d := openTestDB(t)
	rep := verify(t, d)
	if !rep.OK {
		t.Errorf("empty table should verify, got break at %q", rep.BreakID)
	}
	if rep.VerifiableFrom != "" {
		t.Errorf("VerifiableFrom = %q, want empty", rep.VerifiableFrom)
	}
}

// A naive concatenation hashes ("ab","c") and ("a","bc") identically, so a
// character could be moved between actor_id and action without breaking the
// chain. The length-prefixed encoding must separate them.
func TestHashFieldBoundaryCollisionIsPrevented(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 800, time.UTC)
	base := Entry{
		PrevHash:  "0000000000000000000000000000000000000000000000000000000000000000",
		ActorType: "user",
		TargetID:  "t1",
		Details:   `{"i":"1"}`,
		CreatedAt: at,
	}
	abC := base
	abC.ActorID, abC.Action = "ab", "c"
	aBc := base
	aBc.ActorID, aBc.Action = "a", "bc"

	if entryHashOf(abC) == entryHashOf(aBc) {
		t.Fatal("field-boundary collision: (\"ab\",\"c\") and (\"a\",\"bc\") hash the same")
	}
}

// The same collision across every adjacent pair of free-text fields, not just
// the two the first test names.
func TestHashFieldBoundaryCollisionAcrossAllFields(t *testing.T) {
	at := time.Date(2026, 3, 4, 5, 6, 7, 800, time.UTC)

	// Free-text fields in the order entryHashOf hashes them. created_at is
	// absent on purpose: it is a parsed timestamp, so there is no character to
	// slide across that boundary. A change to it is a change of instant, and
	// that is covered by TestHashIsStableForSameInput.
	fields := []func(e *Entry) *string{
		func(e *Entry) *string { return &e.PrevHash },
		func(e *Entry) *string { return &e.ActorType },
		func(e *Entry) *string { return &e.ActorID },
		func(e *Entry) *string { return &e.Action },
		func(e *Entry) *string { return &e.TargetID },
		func(e *Entry) *string { return &e.Details },
	}
	values := []string{"ab", "user", "actor-1", "device.enroll", "device-9", `{"k":"v"}`}
	names := []string{"prev_hash", "actor_type", "actor_id", "action", "target_id", "details"}

	base := func() Entry {
		e := Entry{CreatedAt: at}
		for i, f := range fields {
			*f(&e) = values[i]
		}
		return e
	}

	for i := 0; i+1 < len(fields); i++ {
		t.Run(names[i]+"/"+names[i+1], func(t *testing.T) {
			orig := base()
			shifted := base()

			joined := values[i] + values[i+1]
			// Move one character of the later field onto the end of the earlier
			// one. The naive concatenation is byte-identical; only the split
			// moves, which is precisely what a length prefix must notice.
			cut := len(values[i]) - 1
			*fields[i](&shifted) = joined[:cut]
			*fields[i+1](&shifted) = joined[cut:]

			if *fields[i](&orig)+*fields[i+1](&orig) != *fields[i](&shifted)+*fields[i+1](&shifted) {
				t.Fatalf("test bug: the concatenated fields differ, so this is not a boundary move")
			}
			if entryHashOf(orig) == entryHashOf(shifted) {
				t.Errorf("boundary %s/%s not covered by the encoding", names[i], names[i+1])
			}
		})
	}
}

func TestHashIsStableForSameInput(t *testing.T) {
	e := Entry{
		PrevHash:  "0000000000000000000000000000000000000000000000000000000000000000",
		ActorType: "user", ActorID: "u1", Action: "auth.login", TargetID: "t1",
		Details: `{"a":1}`, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC),
	}
	if entryHashOf(e) != entryHashOf(e) {
		t.Fatal("hash not deterministic")
	}
	// Every field must be covered.
	mutations := map[string]func(e *Entry){
		"prev_hash":  func(e *Entry) { e.PrevHash = "1" + e.PrevHash[1:] },
		"actor_type": func(e *Entry) { e.ActorType = "agent" },
		"actor_id":   func(e *Entry) { e.ActorID = "u2" },
		"action":     func(e *Entry) { e.Action = "auth.logout" },
		"target_id":  func(e *Entry) { e.TargetID = "t2" },
		"details":    func(e *Entry) { e.Details = `{"a":2}` },
		"created_at": func(e *Entry) { e.CreatedAt = e.CreatedAt.Add(time.Nanosecond) },
	}
	for name, mut := range mutations {
		t.Run(name, func(t *testing.T) {
			m := e
			mut(&m)
			if entryHashOf(m) == entryHashOf(e) {
				t.Errorf("mutating %s did not change the hash", name)
			}
		})
	}
}

// A second process writing the same file is the case the in-process mutex
// cannot help with, and the one BEGIN IMMEDIATE exists for. The helper is
// re-execed as a test binary so a genuinely separate database/sql pool and a
// separate process take part.
func TestCrossProcessChainIsNotForked(t *testing.T) {
	if os.Getenv("AUDIT_XPROC_HELPER") == "1" {
		runXProcHelper(t)
		return
	}

	d := openTestDB(t)
	logN(t, d, 3)
	dsn := dbFileOf(t, d)

	const children = 3
	const perChild = 5
	cmds := make([]*exec.Cmd, children)
	for i := range cmds {
		cmds[i] = exec.Command(os.Args[0],
			"-test.run", "TestCrossProcessChainIsNotForked", "-test.timeout", "120s")
		cmds[i].Env = append(os.Environ(),
			"AUDIT_XPROC_HELPER=1",
			"AUDIT_XPROC_DSN="+dsn,
			fmt.Sprintf("AUDIT_XPROC_TAG=%d", i),
			fmt.Sprintf("AUDIT_XPROC_N=%d", perChild))
		cmds[i].Stdout, cmds[i].Stderr = os.Stdout, os.Stderr
	}
	for _, c := range cmds {
		if err := c.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
	}
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("helper: %v", err)
		}
	}

	rep := verify(t, d)
	if !rep.OK {
		t.Fatalf("separate processes forked the chain at %s: %s", rep.BreakID, rep.Reason)
	}
	if want := 3 + children*perChild; rep.Checked != want {
		t.Errorf("Checked = %d, want %d; an entry was lost across processes", rep.Checked, want)
	}
}

func runXProcHelper(t *testing.T) {
	dsn := os.Getenv("AUDIT_XPROC_DSN")
	tag := os.Getenv("AUDIT_XPROC_TAG")
	n, _ := strconv.Atoi(os.Getenv("AUDIT_XPROC_N"))

	// A second pool over the same file, with several connections, is what makes
	// this a separate writer rather than a second goroutine on the first.
	d, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SetMaxOpenConns(4)
	if _, err := d.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := Log(context.Background(), d, "agent", "xproc-"+tag,
			"xproc.action", fmt.Sprintf("%s-%d", tag, i), nil); err != nil {
			t.Fatalf("helper Log: %v", err)
		}
	}
}

// dbFileOf recovers the on-disk path of an opened database, needed to let a
// child process open the same file.
func dbFileOf(t *testing.T, d *sqlx.DB) string {
	t.Helper()
	var name string
	if err := d.Get(&name,
		`SELECT file FROM pragma_database_list WHERE name = 'main'`); err != nil {
		t.Fatalf("read database path: %v", err)
	}
	if name == "" {
		t.Fatal("database has no on-disk path")
	}
	return name
}

// The pool is opened up beyond the single connection db.Open configures on
// purpose. With MaxOpenConns(1) the pool hands out one connection at a time,
// which serialises the writers before they ever reach the transaction, and the
// test would pass even with both the mutex and BEGIN IMMEDIATE deleted. That
// would be a test that proves the pool rather than the chain.
func TestConcurrentLogProducesVerifyingChain(t *testing.T) {
	d := openTestDB(t)
	d.SetMaxOpenConns(16)
	ctx := context.Background()

	const writers = 16
	const perWriter = 8

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	start := make(chan struct{})
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for i := 0; i < perWriter; i++ {
				if err := Log(ctx, d, "agent", fmt.Sprintf("dev-%d", w),
					"concurrent.action", fmt.Sprintf("t-%d-%d", w, i),
					map[string]string{"w": fmt.Sprint(w), "i": fmt.Sprint(i)}); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Log: %v", err)
	}

	rep := verify(t, d)
	if !rep.OK {
		t.Fatalf("concurrent writes broke the chain at %s: %s", rep.BreakID, rep.Reason)
	}
	if rep.Checked != writers*perWriter {
		t.Errorf("Checked = %d, want %d; an entry was lost or double counted", rep.Checked, writers*perWriter)
	}

	// Every entry present exactly once, and every prev_hash distinct: a fork
	// would show up as two rows claiming the same predecessor.
	var total, chained int
	if err := d.GetContext(ctx, &total, `SELECT COUNT(*) FROM audit_logs`); err != nil {
		t.Fatal(err)
	}
	if err := d.GetContext(ctx, &chained, `SELECT COUNT(DISTINCT prev_hash) FROM audit_logs WHERE entry_hash <> ''`); err != nil {
		t.Fatal(err)
	}
	if total != writers*perWriter {
		t.Errorf("table has %d rows, want %d", total, writers*perWriter)
	}
	if chained != writers*perWriter {
		t.Errorf("%d distinct prev_hash for %d entries; the chain forked", chained, writers*perWriter)
	}
}

// Two handles on different databases must not share chain state: a leak would
// anchor one database's chain to rows in the other.
func TestChainStateIsPerHandle(t *testing.T) {
	ctx := context.Background()
	d1 := openTestDB(t)
	d2 := openTestDB(t)

	logN(t, d1, 3)
	logN(t, d2, 3)

	// A caller reading the tail of d1 must never see d2's hash.
	for _, d := range []*sqlx.DB{d1, d2} {
		conn, err := d.Connx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		prev, _, err := tailHash(ctx, conn)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		var own string
		if err := d.GetContext(ctx, &own,
			`SELECT entry_hash FROM audit_logs ORDER BY rowid DESC LIMIT 1`); err != nil {
			t.Fatal(err)
		}
		if prev != own {
			t.Errorf("tail for one handle = %q, want its own %q", prev, own)
		}
	}
	if r1, r2 := verify(t, d1), verify(t, d2); !r1.OK || !r2.OK {
		t.Errorf("cross-handle contamination: d1 ok=%v %s, d2 ok=%v %s",
			r1.OK, r1.Reason, r2.OK, r2.Reason)
	}
}

// An out-of-band writer (a restore, a test, an operator at the sqlite3 prompt)
// must not be able to leave the chain anchored to a stale row. There is no
// cache, so the tail is always read back.
func TestOutOfBandInsertDoesNotCorruptTheChain(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	logN(t, d, 3)

	// Someone appends a properly chained row without going through Log. It
	// links to the real tail, so the chain stays intact...
	conn, err := d.Connx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prev, prevAt, err := tailHash(ctx, conn)
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	rogue := Entry{ID: "rogue", ActorType: "user", ActorID: "u1", Action: "rogue.action",
		TargetID: "t1", Details: `{}`, CreatedAt: prevAt.Add(time.Millisecond), PrevHash: prev}
	rogue.EntryHash = entryHashOf(rogue)
	if _, err := d.ExecContext(ctx,
		`INSERT INTO audit_logs (id, actor_type, actor_id, action, target_id, details,
		        created_at, prev_hash, entry_hash) VALUES (?,?,?,?,?,?,?,?,?)`,
		rogue.ID, rogue.ActorType, rogue.ActorID, rogue.Action, rogue.TargetID,
		rogue.Details, rogue.CreatedAt, rogue.PrevHash, rogue.EntryHash); err != nil {
		t.Fatal(err)
	}

	// ...and the next Log chains onto it, not past it.
	if err := Log(ctx, d, "user", "u1", "after.rogue", "t1", nil); err != nil {
		t.Fatal(err)
	}
	rep := verify(t, d)
	if !rep.OK {
		t.Fatalf("out-of-band insert broke the chain at %s: %s", rep.BreakID, rep.Reason)
	}
	if rep.Checked != 5 {
		t.Errorf("Checked = %d, want 5", rep.Checked)
	}
}

// A failed write must not leave a transaction open holding SQLite's write lock,
// which would stall every other writer on the handle.
func TestFailedWriteDoesNotLeakWriteLock(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	logN(t, d, 2)

	// A duplicate id fails the INSERT after the tail has been read.
	if err := Log(ctx, d, "user", "u1", "x", "y", nil); err != nil {
		t.Fatal(err)
	}
	var existing string
	if err := d.GetContext(ctx, &existing,
		`SELECT id FROM audit_logs WHERE action = 'x'`); err != nil {
		t.Fatal(err)
	}

	// Now force a failure inside the transaction and confirm the handle still
	// works afterwards.
	failing := d
	if _, err := failing.ExecContext(ctx,
		`INSERT INTO audit_logs (id, actor_type, action, created_at) VALUES (?, 'user','a',?)`,
		existing, time.Now().UTC()); err == nil {
		t.Fatal("expected the duplicate insert to fail")
	}

	done := make(chan error, 1)
	go func() { done <- Log(ctx, d, "user", "u1", "after.failure", "t1", nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write after a failed insert: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("write blocked after a failed insert: the write lock leaked")
	}

	if rep := verify(t, d); !rep.OK {
		t.Errorf("chain broken after a failed insert: %s", rep.Reason)
	}
}

// List must keep returning what it returned before the chain columns existed.
func TestListStillWorks(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if err := Log(ctx, d, "user", "u1", "a.b", "t1", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	entries, err := List(ctx, d, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Action != "a.b" || e.ActorID != "u1" || e.TargetID != "t1" || e.Details != `{"k":"v"}` {
		t.Errorf("List returned unexpected row: %+v", e)
	}
	if e.EntryHash == "" {
		t.Error("List returned no entry_hash; the column is selected implicitly by the struct")
	}
}

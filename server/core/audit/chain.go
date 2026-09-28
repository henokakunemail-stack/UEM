package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

// genesisHash is the prev_hash of the first chained entry. Anchoring the chain
// here is what makes "the first row was deleted" and "a row was inserted before
// the first row" distinguishable from a chain that legitimately has no
// predecessor.
const genesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// hashVersion tags the encoding. Without it, a change to the canonical form
// later would silently reinterpret every hash already stored, and VerifyChain
// would report a break that nobody made.
const hashVersion = "audit-chain-v1"

// ChainReport is the result of walking the audit log.
type ChainReport struct {
	// OK is true when every chained row links to its predecessor and recomputes.
	OK bool `json:"ok"`
	// VerifiableFrom is the id of the first row carrying a chain hash. Rows
	// written before migration 0016 have an empty entry_hash and cannot be
	// chained retroactively, so they lie outside what this report covers. Empty
	// when the table holds no chained row at all.
	VerifiableFrom string `json:"verifiable_from"`
	// VerifiableFromRowid is the rowid of VerifiableFrom, for paging.
	VerifiableFromRowid int64 `json:"verifiable_from_rowid"`
	// Checked counts the chained rows verified.
	Checked int `json:"checked"`
	// BreakID is the entry at which the chain first fails to hold. For a
	// deletion this is the surviving row that exposed the gap, since the
	// deleted row can no longer be read to name it. Empty when OK.
	BreakID string `json:"break_id"`
	// Missing distinguishes the two break shapes. A deleted row leaves its
	// successor still pointing at the hash that is now gone; an edited row no
	// longer hashes to the entry_hash it carries.
	Missing bool `json:"missing"`
	// Reason is a short human-readable description of the break.
	Reason string `json:"reason"`
}

// chainLocks is the set of per-handle write locks.
//
// It is keyed by *sqlx.DB because several independent databases exist in this
// process: tests each open their own, and a deployment can hold a second handle
// on a restored backup. The key point is that a lock is the only thing shared
// between handles, and a wrongly-shared lock can only make two unrelated writes
// wait for each other. It cannot make a chain link to a row in the wrong
// database, because the row to link to is read from the handle the caller passed
// in, on that handle's own transaction. Any per-handle state that fed the hash
// would have to be scoped just as carefully.
var (
	chainMu    sync.Mutex
	chainLocks = make(map[*sqlx.DB]*sync.Mutex)
)

// chainLockFor returns the write lock belonging to exactly this handle.
func chainLockFor(db *sqlx.DB) *sync.Mutex {
	chainMu.Lock()
	defer chainMu.Unlock()
	l, ok := chainLocks[db]
	if !ok {
		l = &sync.Mutex{}
		chainLocks[db] = l
	}
	return l
}

// writeChained appends one entry to the chain.
//
// Reading the previous hash and inserting the successor must not be separable
// steps. The entry hash needs the previous entry's hash, so anything that lets
// another writer run between the SELECT and the INSERT lets two of them read the
// same prev_hash and both append onto it, leaving two entries claiming one
// predecessor. VerifyChain would then report a break on a log nobody tampered
// with, and a tamper-evident log that cries wolf is one an operator learns to
// ignore.
//
// The whole read-prev-plus-insert runs in one BEGIN IMMEDIATE transaction on one
// connection, which is what makes the pair atomic. IMMEDIATE takes SQLite's
// write lock at BEGIN rather than promoting a deferred transaction at first
// write, so the SELECT is already inside the write transaction and cannot see a
// tail that a concurrent commit moves out from under it. A deferred BEGIN would
// let two callers both read the same tail and only collide on the INSERT, at
// which point the loser retries the whole transaction with a prev_hash it no
// longer wants.
//
// The per-handle mutex on top of that is queueing, not correctness. db.Open
// pins MaxOpenConns(1) and sets busy_timeout=5000, so without it, concurrent
// in-process writers would block inside SQLite and could time out with
// SQLITE_BUSY, which would drop an audit entry. Failing to record an audit
// entry is worse than a slower one. The mutex makes them wait in Go instead,
// where they cannot be refused. It is per handle, so one database's backlog
// never blocks another's.
//
// There is deliberately no cache of the previous hash. A cached tail is only
// sound if every writer goes through this function, and that is a property of
// the callers rather than of this package: a restored backup, a test inserting
// rows directly, or an operator at the sqlite3 prompt all leave the cache
// pointing at a row that is no longer the newest, and the next write would chain
// onto a stale hash. That is not drift that repairs itself, it is a permanent
// false break that no reader of the report can explain. One indexed single-row
// lookup per audit write is cheap on an append-only table, and it is the only
// version of this that cannot quietly lie.
func writeChained(ctx context.Context, db *sqlx.DB, e Entry) error {
	l := chainLockFor(db)
	l.Lock()
	defer l.Unlock()

	// A dedicated connection, because transaction control is issued by hand.
	conn, err := db.Connx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	// Not BeginTxx: the driver emits its own "begin" on BeginTx, and a nested
	// BEGIN IMMEDIATE is an error, which would cost the write lock this depends
	// on. Raw statements also keep the transaction on this one connection
	// instead of the pool, so an unrelated query cannot interleave into it.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	open := true
	defer func() {
		if open {
			// Best effort. The rows are uncommitted and the connection is going
			// back to the pool, so leaving the transaction open would hand the
			// next caller a connection still holding SQLite's write lock.
			//
			// Rolled back on a context detached from the caller's, because a
			// cancelled request must still clean up, and cleanup must not inherit
			// a request's deadline that has already expired.
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	prev, prevAt, err := tailHash(ctx, conn)
	if err != nil {
		return err
	}

	// The timestamp is taken here, under the lock, rather than by the caller.
	// A caller that stamps the time before it blocks can commit its row after a
	// row carrying a later timestamp, and VerifyChain, which walks by created_at,
	// would reach this row first and find a prev_hash matching nothing. That is
	// a false break on an untampered log. Taking it here, and never letting it
	// go backwards, makes walk order and chain order the same order by
	// construction instead of by luck. Windows clock granularity is coarse
	// enough that the ties this fixes are common, not exotic.
	row := e
	row.CreatedAt = time.Now().UTC()
	if row.CreatedAt.Before(prevAt) {
		row.CreatedAt = prevAt
	}
	row.PrevHash = prev
	row.EntryHash = entryHashOf(row)

	if _, err := conn.ExecContext(ctx, `
		INSERT INTO audit_logs (id, actor_type, actor_id, action, target_id, details,
		                        created_at, prev_hash, entry_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.ActorType, row.ActorID, row.Action, row.TargetID,
		nullDetails(row.Details), row.CreatedAt, row.PrevHash, row.EntryHash); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	open = false
	return nil
}

// tailHash returns the newest chained entry's hash and the timestamp it belongs
// to, or genesis when the table holds no chained entry.
func tailHash(ctx context.Context, conn *sqlx.Conn) (string, time.Time, error) {
	var last string
	var at time.Time
	err := conn.QueryRowxContext(ctx,
		`SELECT entry_hash, created_at FROM audit_logs
		  WHERE entry_hash <> '' ORDER BY created_at DESC, rowid DESC LIMIT 1`).Scan(&last, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return genesisHash, time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, err
	}
	if last == "" {
		return genesisHash, time.Time{}, nil
	}
	return last, at.UTC(), nil
}

// nullDetails keeps a nil details argument NULL, which is what the pre-chain code
// stored. actor_id and target_id pass through unchanged for the same reason: the
// reports export deliberately distinguishes "no target" from an empty one.
func nullDetails(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// entryHashOf is the canonical hash of one chained row.
//
// The encoding length-prefixes and tags every field instead of concatenating
// them. Concatenation makes ("ab","c") and ("a","bc") the same byte string, so
// anyone with write access to the table could move characters across a field
// boundary -- shift a character off the end of an actor_id and onto the front of
// an action, or split a details blob in two -- and every hash would still
// recompute, because the hash never saw where one field ended and the next
// began. Prefixing each field with its own length makes that boundary part of
// what is hashed, and the tag stops a field added later from reproducing an
// older byte string.
func entryHashOf(e Entry) string {
	var buf bytes.Buffer
	buf.WriteString(hashVersion)
	fields := [][2]string{
		{"prev_hash", e.PrevHash},
		{"actor_type", e.ActorType},
		{"actor_id", e.ActorID},
		{"action", e.Action},
		{"target_id", e.TargetID},
		{"details", e.Details},
		{"created_at", e.CreatedAt.UTC().Format(time.RFC3339Nano)},
	}
	var n [8]byte
	for _, f := range fields {
		binary.BigEndian.PutUint32(n[:4], uint32(len(f[0])))
		buf.Write(n[:4])
		buf.WriteString(f[0])
		binary.BigEndian.PutUint64(n[:], uint64(len(f[1])))
		buf.Write(n[:])
		buf.WriteString(f[1])
	}
	sum := sha256.Sum256(buf.Bytes())
	return hexEncode(sum[:])
}

// verifyRow is a row of audit_logs in walk order.
type verifyRow struct {
	RowID     int64     `db:"rowid"`
	ID        string    `db:"id"`
	ActorType string    `db:"actor_type"`
	ActorID   string    `db:"actor_id"`
	Action    string    `db:"action"`
	TargetID  string    `db:"target_id"`
	Details   string    `db:"details"`
	CreatedAt time.Time `db:"created_at"`
	PrevHash  string    `db:"prev_hash"`
	EntryHash string    `db:"entry_hash"`
}

func (r verifyRow) entry() Entry {
	return Entry{
		ID:        r.ID,
		ActorType: r.ActorType,
		ActorID:   r.ActorID,
		Action:    r.Action,
		TargetID:  r.TargetID,
		Details:   r.Details,
		CreatedAt: r.CreatedAt,
		PrevHash:  r.PrevHash,
		EntryHash: r.EntryHash,
	}
}

// VerifyChain walks audit_logs and reports the first break in the hash chain.
//
// created_at alone cannot order the walk. Two entries can share a single
// timestamp, and a tie broken arbitrarily would make a clean log report a break.
// rowid is the tiebreak, and it is the same key writeChained appends by.
func VerifyChain(ctx context.Context, db *sqlx.DB) (ChainReport, error) {
	var rows []verifyRow
	err := db.SelectContext(ctx, &rows, `
		SELECT rowid, id, actor_type, COALESCE(actor_id, '') AS actor_id, action,
		       COALESCE(target_id, '') AS target_id, COALESCE(details, '') AS details,
		       created_at, prev_hash, entry_hash
		  FROM audit_logs
		 ORDER BY created_at ASC, rowid ASC`)
	if err != nil {
		return ChainReport{}, err
	}

	rep := ChainReport{OK: true}

	// Rows written before migration 0016 carry an empty entry_hash. They are
	// skipped rather than reported as breaks: they were never chained, so the
	// missing link there is the migration, not tampering. Chaining them would
	// mean rewriting history, which is the one thing this feature exists to
	// make impossible.
	first := -1
	for i, r := range rows {
		if r.EntryHash != "" {
			first = i
			break
		}
	}
	if first < 0 {
		rep.Reason = "no chained entries"
		return rep, nil
	}
	rep.VerifiableFrom = rows[first].ID
	rep.VerifiableFromRowid = rows[first].RowID

	// Which entry hashes still exist in the table. Needed at the end of the
	// walk, and collected up front because the walk stops at the break and the
	// row that produced the dangling hash may be the very row just examined.
	present := make(map[string]struct{}, len(rows)-first)
	for _, r := range rows[first:] {
		present[r.EntryHash] = struct{}{}
	}

	// The first chained row is anchored on whatever prev_hash it recorded, and
	// the walk checks it and everything after. The region is verifiable from its
	// own start, and everything before it is already declared out of scope by
	// VerifiableFrom, so there is nothing to check there.
	prevHash := rows[first].PrevHash
	for i := first; i < len(rows); i++ {
		cur := rows[i]
		rep.Checked++

		recomputes := entryHashOf(cur.entry()) == cur.EntryHash
		if cur.PrevHash == prevHash && recomputes {
			prevHash = cur.EntryHash
			continue
		}

		rep.OK = false
		rep.BreakID = cur.ID
		switch {
		case !recomputes:
			// The row's own contents no longer hash to the entry_hash it
			// carries, so this row is the one that changed. Definitive: nothing
			// else would make a row disagree with its own hash.
			rep.Reason = fmt.Sprintf("entry_hash does not recompute: stored %s, computed %s",
				short(cur.EntryHash), short(entryHashOf(cur.entry())))

		case !presentIn(present, cur.PrevHash):
			// The row is internally consistent, so it was not touched, but the
			// hash it claims to follow is not produced by any row in the table.
			// The predecessor it was written against is gone in that form.
			//
			// That covers a deleted row and a rewritten one alike, and no
			// comparison of surviving rows can tell them apart: a re-hashed
			// forgery is designed to leave exactly this trace. Both are reported
			// as a break at the row that noticed; Missing here means "the
			// predecessor no longer hashes to what this row expects", not a
			// claim about which of the two happened.
			rep.Missing = true
			rep.Reason = fmt.Sprintf(
				"the entry before %s is gone or was rewritten: it points at %s, which no row in the table produces",
				cur.ID, short(cur.PrevHash))

		default:
			// The hash exists but is not this row's predecessor, so rows were
			// reordered or inserted. Not a deletion: the link resolves.
			rep.Reason = fmt.Sprintf(
				"prev_hash %s is not the previous entry; rows were reordered or inserted",
				short(cur.PrevHash))
		}
		return rep, nil
	}
	rep.Reason = "chain verified"
	return rep, nil
}

func presentIn(m map[string]struct{}, h string) bool {
	_, ok := m[h]
	return ok
}

func short(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12] + "..."
}
